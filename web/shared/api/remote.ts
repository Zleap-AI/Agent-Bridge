import type {
  AgentInfo,
  ApiKeyInfo,
  CallRecord,
  DeviceInfo,
  MessageInfo,
  PairingCodeInfo,
  ServerStatus,
  SessionInfo,
  StreamEvent,
} from "../types";
import { ApiError, listFrom, requestJSON, streamRequest } from "./http";

type RecordValue = Record<string, unknown>;
const API = "/api/v1";
const MESSAGE_PAGE_SIZE = 100;
const pathPart = (value: string) => encodeURIComponent(value);

const parseTime = (value: unknown): string | number | undefined => {
  return typeof value === "string" || typeof value === "number" ? value : undefined;
};

export function normalizeDevice(value: RecordValue): DeviceInfo {
  const id = String(value.id || value.device_id || value.bridge_id || "");
  const agentList = listFrom(value.agents, "agents");
  return {
    id,
    name: String(value.name || value.device_name || id),
    online: Boolean(value.online ?? value.connected),
    agentCount: Number(value.agent_count ?? value.agentCount ?? agentList.length ?? 0),
    lastSeenAt: parseTime(value.last_seen_at ?? value.lastSeenAt),
  };
}

export function normalizeRemoteMessage(value: RecordValue): MessageInfo {
  const roleValue = String(value.role || "assistant");
  const role = roleValue === "user" || roleValue === "system"
    ? roleValue
    : roleValue === "reasoning" || roleValue === "thought"
      ? "reasoning"
      : "assistant";
  const blocks = Array.isArray(value.content)
    ? value.content.filter((block): block is RecordValue => Boolean(block && typeof block === "object"))
    : [{ type: "text", text: value.text || value.content || "" }];
  return {
    id: String(value.id || "") || undefined,
    role,
    content: blocks.filter((block) => String(block.type || "text") === "text").map((block) => ({ type: "text", text: String(block.text || "") })),
    createdAt: parseTime(value.created_at ?? value.createdAt),
  };
}

function normalizeAgent(value: RecordValue): AgentInfo {
  const id = String(value.id || value.agent_id || "");
  const rawStatus = String(value.status || "unknown");
  const status = rawStatus === "idle" || rawStatus === "busy" || rawStatus === "disconnected" || rawStatus === "error" ? rawStatus : "unknown";
  return { id, displayName: String(value.display_name || value.name || id), status };
}

function parseSSEData(data: string): RecordValue {
  try { return JSON.parse(data) as RecordValue; } catch { return { text: data }; }
}

export function normalizeRemoteStreamEvent(event: string, data: string): StreamEvent | null {
  const value = parseSSEData(data);
  if (event === "message.delta") return { type: "message.delta", text: String(value.text || value.delta || "") };
  if (event === "reasoning.delta") return { type: "reasoning.delta", text: String(value.text || value.delta || "") };
  if (event === "session.updated") return { type: "session.updated", sessionId: String(value.session_id || value.sessionId || value.id || "") };
  if (event === "error") {
    const nested = value.error && typeof value.error === "object" ? value.error as RecordValue : {};
    return { type: "error", code: String(nested.code || value.code || "INTERNAL_ERROR"), message: String(nested.message || value.message || "Agent request failed") };
  }
  if (event === "done") return { type: "done" };
  return null;
}

export function normalizeCallStatus(value: unknown): CallRecord["status"] {
  const status = String(value || "error");
  if (status === "success" || status === "completed") return "success";
  if (status === "running") return "running";
  return "error";
}

export const remoteApi = {
  async getStatus(): Promise<ServerStatus> {
    const raw = await requestJSON<RecordValue>(`${API}/status`);
    return {
      initialized: Boolean(raw.initialized ?? raw.setup_complete ?? !raw.setup_required),
      version: String(raw.version || "0.4.0"),
      healthy: String(raw.status || "ok") === "ok" || Boolean(raw.healthy),
    };
  },

  async setup(password: string, token: string): Promise<void> {
    await requestJSON(`${API}/setup`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      body: JSON.stringify({ password }),
    });
  },

  async login(password: string): Promise<void> {
    await requestJSON(`${API}/auth/login`, { method: "POST", body: JSON.stringify({ password }) });
  },

  async logout(): Promise<void> {
    await requestJSON(`${API}/auth/logout`, { method: "POST" });
  },

  async getDevices(signal?: AbortSignal): Promise<DeviceInfo[]> {
    const raw = await requestJSON<unknown>(`${API}/admin/devices`, { signal });
    return listFrom<RecordValue>(raw, "devices", "items").map(normalizeDevice).filter((device) => device.id);
  },

  async renameDevice(id: string, name: string): Promise<DeviceInfo> {
    const raw = await requestJSON<RecordValue>(`${API}/admin/devices/${pathPart(id)}`, {
      method: "PATCH",
      body: JSON.stringify({ name }),
    });
    return normalizeDevice((raw.device || raw) as RecordValue);
  },

  async deleteDevice(id: string): Promise<void> {
    await requestJSON(`${API}/admin/devices/${pathPart(id)}`, { method: "DELETE" });
  },

  async getAgents(deviceId: string, signal?: AbortSignal): Promise<AgentInfo[]> {
    const raw = await requestJSON<unknown>(`${API}/devices/${pathPart(deviceId)}/agents`, { signal });
    return listFrom<RecordValue>(raw, "agents", "items").map(normalizeAgent).filter((agent) => agent.id);
  },

  async getSessions(deviceId: string, agentId: string, signal?: AbortSignal): Promise<SessionInfo[]> {
    const raw = await requestJSON<unknown>(`${API}/devices/${pathPart(deviceId)}/agents/${pathPart(agentId)}/sessions`, { signal });
    return listFrom<RecordValue>(raw, "sessions", "items").map((session) => ({
      id: String(session.id || session.session_id || session.sessionId || ""),
      agentId,
      messageCount: Number(session.message_count || session.messageCount || 0),
      createdAt: parseTime(session.created_at ?? session.createdAt),
      updatedAt: parseTime(session.updated_at ?? session.updatedAt),
    })).filter((session) => session.id);
  },

  async createSession(deviceId: string, agentId: string): Promise<SessionInfo> {
    const raw = await requestJSON<RecordValue>(`${API}/devices/${pathPart(deviceId)}/agents/${pathPart(agentId)}/sessions`, { method: "POST" });
    const session = (raw.session || raw) as RecordValue;
    const id = String(session.id || session.session_id || session.sessionId || "");
    if (!id) throw new ApiError("The server did not return a Session ID", 0, "INVALID_RESPONSE");
    return { id, agentId, createdAt: parseTime(session.created_at ?? session.createdAt) };
  },

  async getMessages(deviceId: string, agentId: string, sessionId: string, signal?: AbortSignal): Promise<MessageInfo[]> {
    const path = `${API}/devices/${pathPart(deviceId)}/agents/${pathPart(agentId)}/sessions/${pathPart(sessionId)}/messages`;
    const messages: MessageInfo[] = [];
    let cursor = 0;
    while (true) {
      const raw = await requestJSON<RecordValue>(`${path}?cursor=${cursor}&limit=${MESSAGE_PAGE_SIZE}`, { signal });
      messages.push(...listFrom<RecordValue>(raw, "messages", "items").map(normalizeRemoteMessage));

      const total = Number(raw.total);
      const nextCursor = Number(raw.cursor);
      // Older Servers returned a single unpaged list. Keep that response usable,
      // while requiring progress from Servers that advertise pagination.
      if (!Number.isInteger(total) || !Number.isInteger(nextCursor)) return messages;
      if (nextCursor >= total) return messages;
      if (nextCursor <= cursor) throw new ApiError("The server returned an invalid Message cursor", 0, "INVALID_RESPONSE");
      cursor = nextCursor;
    }
  },

  async streamMessage(
    deviceId: string,
    agentId: string,
    sessionId: string,
    text: string,
    onEvent: (event: StreamEvent) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    let completed = false;
    await streamRequest(
      `${API}/devices/${pathPart(deviceId)}/agents/${pathPart(agentId)}/sessions/${pathPart(sessionId)}/messages`,
      { content: [{ type: "text", text }] },
      ({ event, data }) => {
        const normalized = normalizeRemoteStreamEvent(event, data);
        if (!normalized) return;
        onEvent(normalized);
        if (normalized.type === "error" || normalized.type === "done") completed = true;
      },
      signal,
    );
    if (!completed) onEvent({ type: "done" });
  },

  async createPairingCode(): Promise<PairingCodeInfo> {
    const raw = await requestJSON<RecordValue>(`${API}/admin/pairing-codes`, { method: "POST" });
    const pairing = (raw.pairing_code || raw.pairing || raw) as RecordValue;
    const expiresIn = Number(pairing.expires_in || 600);
    return {
      code: String(pairing.code || pairing.pairing_code || ""),
      expiresAt: parseTime(pairing.expires_at) || Date.now() + expiresIn * 1000,
      consumed: Boolean(pairing.consumed),
    };
  },

  async getApiKeys(): Promise<ApiKeyInfo[]> {
    const raw = await requestJSON<unknown>(`${API}/admin/api-keys`);
    return listFrom<RecordValue>(raw, "api_keys", "keys", "items").map((key) => ({
      id: String(key.id || ""),
      name: String(key.name || ""),
      prefix: String(key.prefix || ""),
      createdAt: parseTime(key.created_at),
      lastUsedAt: parseTime(key.last_used_at),
    })).filter((key) => key.id);
  },

  async createApiKey(name: string): Promise<ApiKeyInfo> {
    const raw = await requestJSON<RecordValue>(`${API}/admin/api-keys`, { method: "POST", body: JSON.stringify({ name }) });
    const key = (raw.api_key || raw) as RecordValue;
    return {
      id: String(key.id || ""),
      name: String(key.name || name),
      prefix: String(key.prefix || ""),
      key: String(key.key || key.token || ""),
      createdAt: parseTime(key.created_at),
    };
  },

  async deleteApiKey(id: string): Promise<void> {
    await requestJSON(`${API}/admin/api-keys/${pathPart(id)}`, { method: "DELETE" });
  },

  async getCalls(): Promise<CallRecord[]> {
    const raw = await requestJSON<unknown>(`${API}/admin/calls`);
    return listFrom<RecordValue>(raw, "calls", "items").map((call) => {
      return {
        id: String(call.id || "") || undefined,
        deviceId: String(call.device_id || ""),
        deviceName: String(call.device_name || "") || undefined,
        agentId: String(call.agent_id || ""),
        status: normalizeCallStatus(call.status),
        durationMs: Number(call.duration_ms ?? call.duration ?? 0),
        createdAt: parseTime(call.created_at) || new Date().toISOString(),
        errorCode: String(call.error_code || "") || undefined,
      };
    });
  },
};
