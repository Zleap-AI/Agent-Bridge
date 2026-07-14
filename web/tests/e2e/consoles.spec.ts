import { expect, test, type Page, type Route } from "@playwright/test";

const json = (route: Route, body: unknown, status = 200) => route.fulfill({
  status,
  contentType: "application/json",
  body: JSON.stringify(body),
});

async function useEnglish(page: Page) {
  await page.addInitScript(() => localStorage.setItem("agent-bridge-language", "en"));
}

async function mockLocal(page: Page) {
  const agents = [
    { agent_id: "codex", display_name: "Codex CLI", status: "idle" },
    { agent_id: "claude-code", display_name: "Claude Code", status: "disconnected" },
  ];
  await page.route("**/api/v1/local/status", (route) => json(route, {
    version: "0.4.0",
    local: { status: "ok", address: "127.0.0.1:9202" },
    remote: { paired: false, connected: false, server_url: "" },
    agents,
  }));
  await page.route("**/agents", (route) => json(route, agents));
  await page.route("**/api/v1/local/pairings", (route) => json(route, {
    remote: { paired: true, connected: false, state: "connecting", server_url: "http://203.0.113.8:9201", device_id: "device-1", device_name: "Studio Mac" },
  }));
  await page.routeWebSocket("**/ws/admin", (socket) => {
    socket.send(JSON.stringify({ method: "bridge/list", params: { bridges: [{ bridge_id: "local", connected: true, agents }] } }));
    socket.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.method === "admin/agents") socket.send(JSON.stringify({ id: message.id, result: agents }));
      if (message.method === "sessions/list") socket.send(JSON.stringify({ id: message.id, result: [{ agent_id: "codex", session_id: "session-one", message_count: 2 }] }));
      if (message.method === "sessions/messages") socket.send(JSON.stringify({ id: message.id, result: { messages: [{ role: "user", text: "Hello" }, { role: "assistant", text: "Ready to help." }], total: 2 } }));
      if (message.method === "invoke" && message.params?.method === "session/new") socket.send(JSON.stringify({ id: message.id, result: { sessionId: "session-new" } }));
      if (message.method === "invoke" && message.params?.method === "session/prompt") {
        socket.send(JSON.stringify({ method: "session/update", params: { request_id: message.id, type: "session_refreshed", content: { text: "session-refreshed" } } }));
        socket.send(JSON.stringify({ method: "session/update", params: { request_id: message.id, type: "response", content: { text: "Streamed answer" } } }));
        socket.send(JSON.stringify({ method: "session/update", params: { request_id: message.id, type: "final", content: {} } }));
      }
    });
  });
}

async function mockRemote(page: Page) {
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => json(route, { devices: [
    { id: "device-1", name: "Studio Mac", online: true, agent_count: 2, last_seen_at: new Date().toISOString() },
    { id: "device-2", name: "Archive PC", online: false, agent_count: 4, last_seen_at: new Date(Date.now() - 60_000).toISOString() },
  ] }));
  await page.route("**/api/v1/admin/pairing-codes", (route) => json(route, { code: "ABCD-1234", expires_at: Date.now() + 600_000, expires_in: 600 }));
  await page.route("**/api/v1/admin/api-keys", (route) => {
    if (route.request().method() === "POST") return json(route, { api_key: { id: "key-2", name: "Test client", prefix: "ab_live_", created_at: new Date().toISOString(), key: "ab_live_secret-value" } });
    return json(route, { api_keys: [{ id: "key-1", name: "Existing client", prefix: "ab_live_", created_at: new Date().toISOString() }] });
  });
  await page.route("**/api/v1/admin/calls", (route) => json(route, { calls: [{ id: "call-1", device_id: "device-1", agent_id: "codex", status: "completed", duration_ms: 845, created_at: new Date().toISOString() }] }));
  await page.route("**/api/v1/devices/device-1/agents", (route) => json(route, { agents: [{ id: "codex", display_name: "Codex CLI", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-1/agents/codex/sessions", (route) => {
    if (route.request().method() === "POST") return json(route, { session: { id: "session-new", device_id: "device-1", agent_id: "codex" } });
    return json(route, { sessions: [{ id: "remote-session", message_count: 2 }] });
  });
  await page.route(/\/api\/v1\/devices\/device-1\/agents\/codex\/sessions\/[^/]+\/messages(?:\?.*)?$/, (route) => {
    if (route.request().method() === "POST") {
      return route.fulfill({
        status: 200,
        headers: { "content-type": "text/event-stream", "cache-control": "no-cache" },
        body: "event: session.updated\ndata: {\"session_id\":\"remote-session-refreshed\"}\n\nevent: message.delta\ndata: {\"text\":\"Remote answer\"}\n\nevent: done\ndata: {}\n\n",
      });
    }
    return json(route, { messages: [{ role: "user", content: [{ type: "text", text: "Hello remotely" }] }, { role: "assistant", content: [{ type: "text", text: "Connected." }] }] });
  });
}

test("Local Console supports the core Agent and Message flow", async ({ page }) => {
  await useEnglish(page);
  await mockLocal(page);
  await page.goto("http://127.0.0.1:4202/");
  await expect(page.getByText("Codex CLI").first()).toBeVisible();
  await expect(page.getByText("Ready to help.")).toBeVisible();
  await page.getByRole("textbox", { name: "Write a Message" }).fill("Test the stream");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Streamed answer")).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("session-refreshed");
  await page.getByRole("button", { name: "Remote connection" }).click();
  await page.getByLabel("Server address").fill("http://203.0.113.8:9201");
  await page.getByLabel("Pairing Code").fill("ABCD-1234");
  await page.getByRole("button", { name: "Connect Server" }).click();
  await expect(page.getByText("Remote connection configured")).toBeVisible();
});

test("Local Console can load an existing native Session by ID", async ({ page }) => {
  await useEnglish(page);
  await mockLocal(page);
  await page.goto("http://127.0.0.1:4202/");

  await expect(page.getByText("Codex CLI").first()).toBeVisible();
  await page.getByRole("button", { name: "Load existing Session" }).click();
  const loader = page.getByRole("dialog", { name: "Load existing Session" });
  await loader.getByLabel("Session ID").fill("native-session-1");
  await loader.getByRole("button", { name: "Load Session" }).click();

  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("native-session-1");
  await expect(page.getByText("Ready to help.")).toBeVisible();
  await expect(loader).toHaveCount(0);
});

test("Remote Console renders a connected Device and streams a Message", async ({ page }) => {
  await useEnglish(page);
  await mockRemote(page);
  await page.goto("http://127.0.0.1:4201/");
  await expect(page.getByText("Studio Mac").first()).toBeVisible();
  const onlineDevice = page.locator("button.sidebar__item").filter({ hasText: "Studio Mac" });
  await expect(onlineDevice).toContainText("Last seen");
  await expect(onlineDevice.locator(".sidebar__item-status-text")).toHaveCSS("white-space", "nowrap");
  await expect(onlineDevice.locator(".sidebar__item-meta")).toHaveText("2");
  const offlineDevice = page.locator("button.sidebar__item").filter({ hasText: "Archive PC" });
  await expect(offlineDevice).toContainText("Last seen");
  await expect(offlineDevice.locator(".sidebar__item-meta")).toHaveText("4");
  await expect(page.getByText("Connected.")).toBeVisible();
  await page.getByRole("textbox", { name: "Write a Message" }).fill("Run remotely");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Remote answer")).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("remote-session-refreshed");
  await page.getByRole("button", { name: "Pair a Device" }).click();
  await page.getByRole("button", { name: "Generate Pairing Code" }).click();
  await expect(page.getByText("ABCD-1234")).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();

  await page.getByRole("button", { name: "API Keys" }).click();
  await expect(page.getByText("Existing client")).toBeVisible();
  const keyName = page.getByLabel("Key name");
  await keyName.fill("😀".repeat(100));
  await expect(keyName).toHaveValue("😀".repeat(100));
  await keyName.fill("x".repeat(101));
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Key name must not exceed 100 characters")).toBeVisible();
  await keyName.fill("Test client");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("ab_live_secret-value")).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();

  await page.getByRole("button", { name: "Settings" }).click();
  await page.getByLabel("Name").fill("😀".repeat(120));
  await expect(page.getByLabel("Name")).toHaveValue("😀".repeat(120));
  await page.getByLabel("Name").fill("x".repeat(121));
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("dialog", { name: "Server Settings" }).getByText("Device name must not exceed 120 characters")).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();

  await page.getByRole("button", { name: "Call history" }).click();
  await expect(page.getByText("Success")).toBeVisible();
  await expect(page.getByRole("dialog").getByText("Studio Mac")).toBeVisible();
});

test("Remote Console documents the complete Caller API flow", async ({ page }) => {
  await useEnglish(page);
  await mockRemote(page);
  await page.goto("http://127.0.0.1:4201/");
  await page.getByRole("button", { name: "API documentation" }).click();

  const docs = page.getByRole("dialog", { name: "API documentation" });
  await expect(docs).toHaveClass(/drawer--wide/);
  await expect(docs.getByRole("heading", { name: "Quick start" })).toBeVisible();
  await expect(docs.getByText("/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages", { exact: true })).toHaveCount(1);
  await expect(docs.getByRole("heading", { name: "SSE events" })).toBeAttached();
  await expect(docs.getByRole("heading", { name: "Error handling" })).toBeAttached();
  await expect(docs.getByText("PAYLOAD_TOO_LARGE", { exact: true })).toHaveCount(1);
  await expect(docs.getByRole("heading", { name: "Runtime rules and limits" })).toBeAttached();
  await expect(docs.getByText("/openapi.json", { exact: true })).toBeAttached();
});

test("Remote Console discovers Agents registered after the Device appears", async ({ page }) => {
  await useEnglish(page);
  let agentRequests = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => json(route, { devices: [{ id: "device-late", name: "New Device", online: true, agent_count: 1 }] }));
  await page.route("**/api/v1/devices/device-late/agents", (route) => {
    agentRequests += 1;
    return json(route, { agents: agentRequests === 1 ? [] : [{ id: "codex", display_name: "Codex CLI", status: "idle" }] });
  });
  await page.route("**/api/v1/devices/device-late/agents/codex/sessions", (route) => json(route, { sessions: [] }));

  await page.goto("http://127.0.0.1:4201/");
  await expect(page.getByText("New Device").first()).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Agents" })).toHaveValue("codex", { timeout: 7000 });
  expect(agentRequests).toBeGreaterThanOrEqual(2);
});

test("both consoles fit a mobile viewport without horizontal overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await useEnglish(page);
  await mockLocal(page);
  await page.goto("http://127.0.0.1:4202/");
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
  await expect(page.getByRole("button", { name: "New Session" })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("Tab");
  const localMenu = page.getByRole("button", { name: "Open navigation" });
  await expect(localMenu).toBeFocused();
  await localMenu.click();
  await expect(page.locator("#app-navigation button.sidebar__item").first()).toBeFocused();
  await expect(page.locator(".shell-column")).toHaveAttribute("inert", "");
  await page.keyboard.press("Escape");
  await expect(page.locator("#app-navigation")).not.toHaveClass(/is-open/);
  await expect(localMenu).toBeFocused();

  await mockRemote(page);
  await page.goto("http://127.0.0.1:4201/");
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  const remoteMenu = page.getByRole("button", { name: "Open navigation" });
  await expect(remoteMenu).toBeVisible();
  await expect(page.getByRole("button", { name: "New Session" })).toBeVisible();
  await remoteMenu.click();
  await expect(page.locator("#app-navigation button.sidebar__item").first()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator("#app-navigation")).not.toHaveClass(/is-open/);
  await expect(remoteMenu).toBeFocused();
});

test("Remote polling ignores an older Device response that finishes last", async ({ page }) => {
  await useEnglish(page);
  let deviceRequests = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", async (route) => {
    deviceRequests += 1;
    if (deviceRequests === 3) {
      await new Promise((resolve) => setTimeout(resolve, 6_000));
      try {
        await json(route, { devices: [{ id: "device-old", name: "Old Device", online: true, agent_count: 0 }] });
      } catch {
        // The new poll is allowed to cancel this obsolete request.
      }
      return;
    }
    const device = deviceRequests >= 4
      ? { id: "device-new", name: "New Device", online: true, agent_count: 0 }
      : { id: "device-stable", name: "Stable Device", online: true, agent_count: 0 };
    await json(route, { devices: [device] });
  });
  await page.route(/\/api\/v1\/devices\/[^/]+\/agents$/, (route) => json(route, { agents: [] }));

  await page.goto("http://127.0.0.1:4201/");
  await expect(page.getByText("Stable Device").first()).toBeVisible();
  await expect(page.getByText("New Device").first()).toBeVisible({ timeout: 12_000 });
  await page.waitForTimeout(1_300);
  await expect(page.getByText("New Device").first()).toBeVisible();
  await expect(page.getByText("Old Device")).toHaveCount(0);
});

test("a hung Session read does not block switching Devices", async ({ page }) => {
  await useEnglish(page);
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => json(route, { devices: [
    { id: "device-hung", name: "Hung Device", online: true, agent_count: 1 },
    { id: "device-healthy", name: "Healthy Device", online: true, agent_count: 1 },
  ] }));
  await page.route("**/api/v1/devices/device-hung/agents", (route) => json(route, { agents: [{ id: "codex", display_name: "Codex", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-healthy/agents", (route) => json(route, { agents: [{ id: "hermes", display_name: "Hermes", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-hung/agents/codex/sessions", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 2_000));
    try { await json(route, { sessions: [{ id: "hung-session" }] }); } catch { /* request was cancelled */ }
  });
  await page.route("**/api/v1/devices/device-healthy/agents/hermes/sessions", (route) => json(route, { sessions: [{ id: "healthy-session" }] }));
  await page.route(/\/api\/v1\/devices\/device-healthy\/agents\/hermes\/sessions\/[^/]+\/messages(?:\?.*)?$/, (route) => json(route, {
    messages: [{ role: "assistant", content: [{ type: "text", text: "Healthy history" }] }], total: 1, cursor: 1,
  }));

  await page.goto("http://127.0.0.1:4201/");
  const healthyDevice = page.locator("button.sidebar__item").filter({ hasText: "Healthy Device" });
  await expect(healthyDevice).toBeEnabled();
  await healthyDevice.click();
  await expect(page.getByRole("combobox", { name: "Agents" })).toHaveValue("hermes");
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("healthy-session");
  await expect(page.getByText("Healthy history")).toBeVisible();
  await page.waitForTimeout(2_100);
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("healthy-session");
});

test("Call history errors stay visible inside the open Drawer", async ({ page }) => {
  await useEnglish(page);
  await mockRemote(page);
  await page.unroute("**/api/v1/admin/calls");
  await page.route("**/api/v1/admin/calls", (route) => json(route, { error: { code: "CALLS_FAILED", message: "Call history unavailable" } }, 503));

  await page.goto("http://127.0.0.1:4201/");
  await page.getByRole("button", { name: "Call history" }).click();
  const drawer = page.getByRole("dialog", { name: "Call history" });
  await expect(drawer.getByText("Call history unavailable")).toBeVisible();
  await expect(page.locator(".top-warning--error")).toHaveCount(0);
});

test("Remote modal focus stays on the top layer and Escape closes one layer", async ({ page }) => {
  await useEnglish(page);
  await mockRemote(page);
  await page.goto("http://127.0.0.1:4201/");
  const apiKeysButton = page.getByRole("button", { name: "API Keys" });
  await apiKeysButton.click();
  const drawer = page.getByRole("dialog", { name: "API Keys" });
  await expect(drawer.getByRole("button", { name: "Close" })).toBeFocused();
  await drawer.getByRole("button", { name: "Delete" }).click();
  const confirmation = page.getByRole("alertdialog");
  await expect(confirmation.getByRole("button", { name: "Cancel" })).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(confirmation).toHaveCount(0);
  await expect(drawer).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);
  await expect(apiKeysButton).toBeFocused();
});

test("a late stream cannot overwrite a Device selected by polling", async ({ page }) => {
  await useEnglish(page);
  let firstDeviceRequestAt = 0;
  let invalidTargetRequests = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => {
    if (!firstDeviceRequestAt) firstDeviceRequestAt = Date.now();
    const device = Date.now() - firstDeviceRequestAt < 4_000
      ? { id: "device-a", name: "Device A", online: true, agent_count: 1 }
      : { id: "device-b", name: "Device B", online: true, agent_count: 1 };
    return json(route, { devices: [device] });
  });
  await page.route("**/api/v1/devices/device-a/agents", (route) => json(route, { agents: [{ id: "codex", display_name: "Codex", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-b/agents", (route) => json(route, { agents: [{ id: "hermes", display_name: "Hermes", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-b/agents/codex/**", (route) => {
    invalidTargetRequests += 1;
    return json(route, { error: { code: "AGENT_NOT_FOUND", message: "Agent was not found" } }, 404);
  });
  await page.route("**/api/v1/devices/device-a/agents/codex/sessions", (route) => json(route, { sessions: [{ id: "session-a" }] }));
  await page.route("**/api/v1/devices/device-b/agents/hermes/sessions", (route) => json(route, { sessions: [{ id: "session-b" }] }));
  await page.route(/\/api\/v1\/devices\/(device-a|device-b)\/agents\/(codex|hermes)\/sessions\/[^/]+\/messages(?:\?.*)?$/, async (route) => {
    const request = route.request();
    if (request.method() === "POST") {
      await new Promise((resolve) => setTimeout(resolve, 5_800));
      return route.fulfill({
        status: 200,
        headers: { "content-type": "text/event-stream", "cache-control": "no-cache" },
        body: "event: session.updated\ndata: {\"session_id\":\"late-session-a\"}\n\nevent: message.delta\ndata: {\"text\":\"Late A response\"}\n\nevent: done\ndata: {}\n\n",
      });
    }
    const deviceB = request.url().includes("device-b");
    return json(route, deviceB
      ? { messages: [{ role: "assistant", content: [{ type: "text", text: "Device B history" }] }], total: 1, cursor: 1 }
      : { messages: [], total: 0, cursor: 0 });
  });

  await page.goto("http://127.0.0.1:4201/");
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("session-a");
  await page.getByRole("textbox", { name: "Write a Message" }).fill("Start on A");
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByText("Device B history")).toBeVisible({ timeout: 8_000 });
  await page.waitForTimeout(1_200);
  await expect(page.getByRole("combobox", { name: "Session" })).toHaveValue("session-b");
  await expect(page.getByText("Late A response")).toHaveCount(0);
  expect(invalidTargetRequests).toBe(0);
});

test("Remote polling clears a transient error after recovery", async ({ page }) => {
  await useEnglish(page);
  let firstDeviceRequestAt = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => {
    if (!firstDeviceRequestAt) firstDeviceRequestAt = Date.now();
    const elapsed = Date.now() - firstDeviceRequestAt;
    if (elapsed >= 4_000 && elapsed < 9_000) {
      return json(route, { error: { code: "INTERNAL_ERROR", message: "Temporary poll outage" } }, 503);
    }
    return json(route, { devices: [{ id: "device-1", name: "Stable Device", online: true, agent_count: 1 }] });
  });
  await page.route("**/api/v1/devices/device-1/agents", (route) => json(route, { agents: [{ id: "codex", display_name: "Codex", status: "idle" }] }));
  await page.route("**/api/v1/devices/device-1/agents/codex/sessions", (route) => json(route, { sessions: [{ id: "session-1" }] }));
  await page.route(/\/api\/v1\/devices\/device-1\/agents\/codex\/sessions\/[^/]+\/messages(?:\?.*)?$/, (route) => json(route, { messages: [], total: 0, cursor: 0 }));

  await page.goto("http://127.0.0.1:4201/");
  await expect(page.getByText("Stable Device").first()).toBeVisible();
  await expect(page.getByText("Temporary poll outage")).toBeVisible({ timeout: 7_000 });
  await expect(page.getByText("Temporary poll outage")).toHaveCount(0, { timeout: 7_000 });
  await expect(page.getByText("Stable Device").first()).toBeVisible();
});

test("Local validation follows the selected language", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("agent-bridge-language", "zh"));
  await mockLocal(page);
  await page.goto("http://127.0.0.1:4202/");
  await page.getByRole("button", { name: "远程连接" }).click();
  await page.getByLabel("Server 地址").fill("not-a-url");
  await page.getByLabel("Pairing Code").fill("ABCD-1234");
  await page.getByRole("button", { name: "连接 Server" }).click();
  await expect(page.getByText("Server 地址必须以 http:// 或 https:// 开头")).toBeVisible();
});

test("fresh Server opens the one-time setup flow", async ({ page }) => {
  await useEnglish(page);
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: false, version: "0.4.0" }));
  await page.goto("http://127.0.0.1:4201/?token=setup-secret");
  await expect(page.getByRole("heading", { name: "Set up Remote Console" })).toBeVisible();
  await expect(page.getByLabel("Setup Token")).toHaveValue("setup-secret");
  await expect(page.getByLabel("Owner Password")).toBeVisible();
});

test("Remote setup counts Unicode code points for the password minimum", async ({ page }) => {
  await useEnglish(page);
  let setupCalls = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: false, version: "0.4.0" }));
  await page.route("**/api/v1/setup", (route) => {
    setupCalls += 1;
    return json(route, { error: { code: "UNAUTHORIZED", message: "Setup rejected by Server" } }, 401);
  });
  await page.goto("http://127.0.0.1:4201/?token=setup-secret");
  const password = page.getByLabel("Owner Password");
  const confirmation = page.getByLabel("Confirm password");
  await password.fill("🔐".repeat(7));
  await confirmation.fill("🔐".repeat(7));
  await page.getByRole("button", { name: "Finish setup" }).click();
  await expect(page.getByText("At least 8 characters")).toBeVisible();
  expect(setupCalls).toBe(0);

  await password.fill("🔐".repeat(8));
  await confirmation.fill("🔐".repeat(8));
  await page.getByRole("button", { name: "Finish setup" }).click();
  await expect(page.getByText("Setup rejected by Server")).toBeVisible();
  expect(setupCalls).toBe(1);
});

test("Remote login requires a value but does not enforce the setup minimum", async ({ page }) => {
  await useEnglish(page);
  let loginCalls = 0;
  await page.route("**/api/v1/status", (route) => json(route, { status: "ok", initialized: true, version: "0.4.0" }));
  await page.route("**/api/v1/admin/devices", (route) => json(route, { error: { code: "UNAUTHORIZED", message: "Authentication required" } }, 401));
  await page.route("**/api/v1/auth/login", (route) => {
    loginCalls += 1;
    return json(route, { error: { code: "UNAUTHORIZED", message: "Login reached Server" } }, 401);
  });
  await page.goto("http://127.0.0.1:4201/");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByText("Enter the Owner Password")).toBeVisible();
  expect(loginCalls).toBe(0);

  await page.getByLabel("Owner Password").fill("短密码");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByText("Login reached Server")).toBeVisible();
  expect(loginCalls).toBe(1);
});
