import { afterEach, describe, expect, it, vi } from "vitest";
import { LocalAdminClient, normalizeAgent, normalizeLocalStatus } from "../shared/api/local";
import { normalizeCallStatus, normalizeDevice, normalizeRemoteMessage, normalizeRemoteStreamEvent, remoteApi } from "../shared/api/remote";
import { listFrom, readSSE, requestJSON } from "../shared/api/http";

describe("API normalization", () => {
  it("normalizes the stable Local status response", () => {
    expect(normalizeLocalStatus({
      version: "0.4.0",
      local: { status: "ok", address: "127.0.0.1:9202" },
      remote: { paired: true, connected: false, server_url: "http://10.0.0.1:9201", device_id: "dev_1" },
    })).toEqual({
      version: "0.4.0",
      localAddress: "127.0.0.1:9202",
      healthy: true,
      agents: [],
      remote: {
        paired: true,
        connected: false,
        state: undefined,
        serverUrl: "http://10.0.0.1:9201",
        deviceId: "dev_1",
        deviceName: undefined,
        lastError: undefined,
      },
    });
  });

  it("normalizes legacy Agent descriptors", () => {
    expect(normalizeAgent({ agent_id: "codex", display_name: "Codex CLI", status: "idle" })).toEqual({
      id: "codex",
      displayName: "Codex CLI",
      status: "idle",
    });
  });

  it("normalizes the legacy health Agent status map", () => {
    expect(normalizeLocalStatus({ status: "ok", agents: { codex: "idle" } }).agents).toEqual([{
      id: "codex",
      displayName: "codex",
      status: "idle",
    }]);
  });

  it("normalizes public Device resources", () => {
    expect(normalizeDevice({ id: "bridge_1", name: "Studio Mac", online: true, agent_count: 11 })).toEqual({
      id: "bridge_1",
      name: "Studio Mac",
      online: true,
      agentCount: 11,
      lastSeenAt: undefined,
    });
  });

  it("keeps only supported text blocks", () => {
    expect(normalizeRemoteMessage({
      role: "assistant",
      content: [{ type: "text", text: "hello" }, { type: "image", url: "ignored" }],
    }).content).toEqual([{ type: "text", text: "hello" }]);
  });

  it("unwraps list envelopes", () => {
    expect(listFrom({ data: { devices: [{ id: "1" }] } }, "devices")).toEqual([{ id: "1" }]);
  });

  it("maps Server call states to the public UI states", () => {
    expect(normalizeCallStatus("completed")).toBe("success");
    expect(normalizeCallStatus("failed")).toBe("error");
    expect(normalizeCallStatus("running")).toBe("running");
  });

  it("reads the structured nested SSE error", () => {
    expect(normalizeRemoteStreamEvent("error", JSON.stringify({ error: { code: "DEVICE_OFFLINE", message: "Device is offline" } }))).toEqual({
      type: "error",
      code: "DEVICE_OFFLINE",
      message: "Device is offline",
    });
  });
});

describe("Remote Message history", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("loads every cursor page", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      const cursor = Number(url.searchParams.get("cursor"));
      const body = cursor === 0
        ? { messages: [{ role: "user", text: "one" }, { role: "assistant", text: "two" }], total: 3, cursor: 2 }
        : { messages: [{ role: "user", text: "three" }], total: 3, cursor: 3 };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);

    const messages = await remoteApi.getMessages("device 1", "agent/1", "session:1");

    expect(messages.map((message) => message.content[0]?.text)).toEqual(["one", "two", "three"]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("cursor=0&limit=100");
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=2&limit=100");
  });
});

describe("HTTP cancellation", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("preserves an explicit caller abort", async () => {
    vi.stubGlobal("fetch", vi.fn((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true });
    })));
    const controller = new AbortController();
    const request = requestJSON("/slow", { signal: controller.signal });

    controller.abort();

    await expect(request).rejects.toMatchObject({ name: "AbortError" });
  });

  it("reports an internal request timeout separately", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true });
    })));
    const request = requestJSON("/slow", {}, 20);
    const assertion = expect(request).rejects.toEqual(expect.objectContaining({
      name: "ApiError",
      code: "TIMEOUT",
    }));

    await vi.advanceTimersByTimeAsync(20);
    await assertion;
  });
});

describe("Local Message history", () => {
  it("loads every cursor page", async () => {
    const client = new LocalAdminClient();
    const request = vi.fn(async (_method: string, params: Record<string, unknown>) => {
      return params.cursor === 0
        ? { messages: [{ role: "user", text: "one" }], total: 2, cursor: 1 }
        : { messages: [{ role: "assistant", text: "two" }], total: 2, cursor: 2 };
    });
    Object.assign(client as unknown as Record<string, unknown>, { request });

    const messages = await client.getMessages("codex", "session-1");

    expect(messages.map((message) => message.content[0]?.text)).toEqual(["one", "two"]);
    expect(request).toHaveBeenNthCalledWith(1, "sessions/messages", {
      agent_id: "codex", session_id: "session-1", cursor: 0, limit: 200,
    });
    expect(request).toHaveBeenNthCalledWith(2, "sessions/messages", {
      agent_id: "codex", session_id: "session-1", cursor: 1, limit: 200,
    });
  });
});

describe("SSE parser", () => {
  it("parses events split across stream chunks", async () => {
    const encoder = new TextEncoder();
    const chunks = [
      encoder.encode("event: message.delta\ndata: {\"text\":\"hel"),
      encoder.encode("lo\"}\n\nevent: done\ndata: {}\n\n"),
    ];
    let index = 0;
    const response = {
      status: 200,
      body: {
        getReader: () => ({
          read: async () => index < chunks.length ? { done: false, value: chunks[index++] } : { done: true, value: undefined },
        }),
      },
    } as unknown as Response;
    const events: Array<{ event: string; data: string }> = [];
    await readSSE(response, (event) => events.push(event));
    expect(events).toEqual([
      { event: "message.delta", data: "{\"text\":\"hello\"}" },
      { event: "done", data: "{}" },
    ]);
  });
});
