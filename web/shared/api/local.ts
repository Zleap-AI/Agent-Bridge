import type { AgentInfo, LocalLogEntry, LocalStatus, MessageInfo, SessionInfo, StreamEvent } from "../types";
import { ApiError, listFrom, requestJSON } from "./http";

type RecordValue = Record<string, unknown>;

export interface LocalSettings {
  debug: boolean;
  claudeSettingsFile: string;
  restartRequired: boolean;
}

const statusValue = (value: unknown): AgentInfo["status"] => {
  return value === "idle" || value === "busy" || value === "disconnected" || value === "error" ? value : "unknown";
};

export function normalizeAgent(value: RecordValue): AgentInfo {
  const id = String(value.agent_id || value.id || "");
  return {
    id,
    displayName: String(value.display_name || value.name || id),
    status: statusValue(value.status),
  };
}

function normalizeStatusAgents(raw: RecordValue): AgentInfo[] {
  const listed = listFrom<RecordValue>(raw, "agents");
  if (listed.length) return listed.map(normalizeAgent).filter((agent) => agent.id);
  const statuses = raw.agents;
  if (!statuses || Array.isArray(statuses) || typeof statuses !== "object") return [];
  return Object.entries(statuses as Record<string, unknown>).map(([id, status]) => normalizeAgent({ agent_id: id, status }));
}

export function normalizeLocalStatus(raw: RecordValue): LocalStatus {
  const local = (raw.local || {}) as RecordValue;
  const remote = (raw.remote || {}) as RecordValue;
  return {
    version: String(raw.version || "0.4.0"),
    localAddress: String(local.address || raw.listen_address || "127.0.0.1:9202"),
    healthy: String(local.status || raw.status || "ok") === "ok",
    agents: normalizeStatusAgents(raw),
    remote: {
      paired: Boolean(remote.paired ?? raw.paired),
      connected: Boolean(remote.connected ?? raw.remote_connected),
      state: String(remote.state || raw.remote_state || "") || undefined,
      serverUrl: String(remote.server_url || raw.server_url || ""),
      deviceId: String(remote.device_id || raw.device_id || "") || undefined,
      deviceName: String(remote.device_name || raw.device_name || "") || undefined,
      lastError: String(remote.last_error || raw.last_error || "") || undefined,
    },
  };
}

export const localApi = {
  async getAgents(): Promise<AgentInfo[]> {
    const raw = await requestJSON<unknown>("/agents");
    return listFrom<RecordValue>(raw, "agents").map(normalizeAgent).filter((agent) => agent.id);
  },

  async getStatus(): Promise<LocalStatus> {
    try {
      const raw = await requestJSON<RecordValue>("/api/v1/local/status");
      return normalizeLocalStatus(raw);
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 404) throw error;
      const health = await requestJSON<RecordValue>("/health");
      return normalizeLocalStatus(health);
    }
  },

  async pair(serverUrl: string, pairingCode: string, replace = false): Promise<LocalStatus> {
    const raw = await requestJSON<RecordValue>("/api/v1/local/pairings", {
      method: "POST",
      body: JSON.stringify({ server_url: serverUrl, pairing_code: pairingCode, replace }),
    });
    return normalizeLocalStatus(raw.remote ? raw : { ...raw, remote: raw });
  },

  async unpair(): Promise<void> {
    await requestJSON("/api/v1/local/pairing", { method: "DELETE" });
  },

  async getLogs(): Promise<LocalLogEntry[]> {
    try {
      const raw = await requestJSON<unknown>("/api/v1/local/logs");
      return listFrom<RecordValue>(raw, "logs", "items").map((entry) => ({
        timestamp: String(entry.timestamp || entry.time || new Date().toISOString()),
        level: entry.level === "debug" || entry.level === "warn" || entry.level === "error" ? entry.level : "info",
        message: String(entry.message || entry.msg || ""),
      }));
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return [];
      throw error;
    }
  },

  async getSettings(): Promise<LocalSettings> {
    try {
      const raw = await requestJSON<RecordValue>("/api/v1/local/settings");
      return {
        debug: Boolean(raw.debug),
        claudeSettingsFile: String(raw.claude_settings_file || ""),
        restartRequired: Boolean(raw.restart_required),
      };
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return { debug: false, claudeSettingsFile: "", restartRequired: false };
      throw error;
    }
  },

  async updateSettings(settings: Pick<LocalSettings, "debug" | "claudeSettingsFile">): Promise<LocalSettings> {
    const raw = await requestJSON<RecordValue>("/api/v1/local/settings", {
      method: "PATCH",
      body: JSON.stringify({ debug: settings.debug, claude_settings_file: settings.claudeSettingsFile }),
    });
    return {
      debug: Boolean(raw.debug ?? settings.debug),
      claudeSettingsFile: String(raw.claude_settings_file ?? settings.claudeSettingsFile),
      restartRequired: Boolean(raw.restart_required),
    };
  },
};

interface RPCResponse {
  id?: string;
  method?: string;
  params?: RecordValue;
  result?: unknown;
  error?: { code?: number; message?: string };
}

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
  timeout: ReturnType<typeof setTimeout>;
}

interface StreamRequest {
  onEvent: (event: StreamEvent) => void;
  resolve: () => void;
  reject: (error: Error) => void;
  timeout: ReturnType<typeof setTimeout>;
}

type ConnectionListener = (connected: boolean) => void;
type AgentListener = (agents: AgentInfo[]) => void;
type LogListener = (level: LocalLogEntry["level"], message: string) => void;

export class LocalAdminClient {
  private socket: WebSocket | null = null;
  private pending = new Map<string, PendingRequest>();
  private streams = new Map<string, StreamRequest>();
  private connectionListeners = new Set<ConnectionListener>();
  private agentListeners = new Set<AgentListener>();
  private logListeners = new Set<LogListener>();
  private sequence = 0;
  private manuallyClosed = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  get connected() { return this.socket?.readyState === WebSocket.OPEN; }

  onConnection(listener: ConnectionListener) { this.connectionListeners.add(listener); return () => this.connectionListeners.delete(listener); }
  onAgents(listener: AgentListener) { this.agentListeners.add(listener); return () => this.agentListeners.delete(listener); }
  onLog(listener: LogListener) { this.logListeners.add(listener); return () => this.logListeners.delete(listener); }

  connect() {
    if (this.socket && this.socket.readyState <= WebSocket.OPEN) return;
    this.manuallyClosed = false;
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${protocol}//${window.location.host}/ws/admin`);
    this.socket = socket;
    socket.addEventListener("open", () => {
      this.emitLog("info", "Local Console connected");
      this.connectionListeners.forEach((listener) => listener(true));
    });
    socket.addEventListener("message", (event) => this.handleMessage(event.data));
    socket.addEventListener("error", () => this.emitLog("error", "Local WebSocket connection error"));
    socket.addEventListener("close", () => {
      if (this.socket === socket) {
        this.connectionListeners.forEach((listener) => listener(false));
        this.rejectAll(new ApiError("Local connection closed", 0, "CONNECTION_CLOSED"));
        this.socket = null;
        if (!this.manuallyClosed) this.reconnectTimer = setTimeout(() => this.connect(), 2000);
      }
    });
  }

  close() {
    this.manuallyClosed = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  }

  async listSessions(agentId: string): Promise<SessionInfo[]> {
    const raw = await this.request("sessions/list", { agent_id: agentId });
    return listFrom<RecordValue>(raw, "sessions").map((session) => ({
      id: String(session.session_id || session.sessionId || session.id || ""),
      agentId: String(session.agent_id || agentId),
      messageCount: Number(session.message_count || 0),
      updatedAt: (session.updated_at || session.updatedAt) as string | number | undefined,
    })).filter((session) => session.id);
  }

  async createSession(agentId: string): Promise<string> {
    const raw = await this.request("invoke", { agent_id: agentId, method: "session/new", params: {} }) as RecordValue;
    const id = String(raw.sessionId || raw.session_id || raw.id || "");
    if (!id) throw new ApiError("The server did not return a Session ID", 0, "INVALID_RESPONSE");
    return id;
  }

  async getMessages(agentId: string, sessionId: string): Promise<MessageInfo[]> {
    const messages: MessageInfo[] = [];
    let cursor = 0;
    while (true) {
      const raw = await this.request("sessions/messages", {
        agent_id: agentId,
        session_id: sessionId,
        cursor,
        limit: 200,
      }) as RecordValue;
      messages.push(...listFrom<RecordValue>(raw, "messages").map(normalizeMessage));

      const total = Number(raw.total);
      const nextCursor = Number(raw.cursor);
      if (!Number.isInteger(total) || !Number.isInteger(nextCursor)) return messages;
      if (nextCursor >= total) return messages;
      if (nextCursor <= cursor) throw new ApiError("The local service returned an invalid Message cursor", 0, "INVALID_RESPONSE");
      cursor = nextCursor;
    }
  }

  streamMessage(
    agentId: string,
    sessionId: string,
    text: string,
    onEvent: (event: StreamEvent) => void,
  ): Promise<void> {
    if (!this.connected || !this.socket) return Promise.reject(new ApiError("Local service is offline", 0, "CONNECTION_CLOSED"));
    const id = this.id("message");
    return new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.streams.delete(id);
        reject(new ApiError("Agent response timed out", 0, "TIMEOUT"));
      }, 10 * 60 * 1000);
      this.streams.set(id, { onEvent, resolve, reject, timeout });
      this.socket?.send(JSON.stringify({
        jsonrpc: "2.0",
        id,
        method: "invoke",
        params: {
          agent_id: agentId,
          method: "session/prompt",
          params: { sessionId, prompt: [{ type: "text", text }] },
          stream: true,
        },
      }));
    });
  }

  private request(method: string, params: RecordValue = {}): Promise<unknown> {
    if (!this.connected || !this.socket) return Promise.reject(new ApiError("Local service is offline", 0, "CONNECTION_CLOSED"));
    const id = this.id(method.replace(/\W/g, "_"));
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.pending.delete(id);
        reject(new ApiError("Local request timed out", 0, "TIMEOUT"));
      }, 30_000);
      this.pending.set(id, { resolve, reject, timeout });
      this.socket?.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
    });
  }

  private handleMessage(raw: unknown) {
    let message: RPCResponse;
    try { message = JSON.parse(String(raw)) as RPCResponse; } catch { this.emitLog("warn", "Ignored an invalid local response"); return; }
    if (message.method === "bridge/list") {
      const bridges = listFrom<RecordValue>(message.params, "bridges");
      const agents = bridges.flatMap((bridge) => listFrom<RecordValue>(bridge.agents, "agents"));
      this.emitAgents(agents);
      return;
    }
    if (message.method === "session/update" && message.params) {
      this.handleStreamUpdate(message.params);
      return;
    }
    if (!message.id) return;

    const stream = this.streams.get(message.id);
    if (stream) {
      if (message.error) this.finishStream(message.id, new ApiError(message.error.message || "Agent request failed", 0, String(message.error.code || "AGENT_ERROR")));
      else if (message.result) {
        const result = message.result as RecordValue;
        const text = String(result.text || result.content || "");
        if (text) stream.onEvent({ type: "message.delta", text });
        stream.onEvent({ type: "done" });
        this.finishStream(message.id);
      }
      return;
    }

    const pending = this.pending.get(message.id);
    if (!pending) return;
    clearTimeout(pending.timeout);
    this.pending.delete(message.id);
    if (message.error) pending.reject(new ApiError(message.error.message || "Local request failed", 0, String(message.error.code || "AGENT_ERROR")));
    else pending.resolve(message.result);
  }

  private handleStreamUpdate(params: RecordValue) {
    const id = String(params.request_id || params.requestId || "");
    const stream = this.streams.get(id);
    if (!stream) return;
    const kind = String(params.type || "");
    const content = (params.content || {}) as RecordValue;
    const text = String(content.text ?? (typeof params.content === "string" ? params.content : ""));
    if (kind === "response" || kind === "message.delta") stream.onEvent({ type: "message.delta", text });
    else if (kind === "thought" || kind === "reasoning.delta") stream.onEvent({ type: "reasoning.delta", text });
    else if (kind === "session_refreshed" || kind === "session.updated") stream.onEvent({ type: "session.updated", sessionId: text || String(content.session_id || "") });
    else if (kind === "error") {
      stream.onEvent({ type: "error", code: "AGENT_ERROR", message: text || "Agent request failed" });
      this.finishStream(id, new ApiError(text || "Agent request failed", 0, "AGENT_ERROR"));
    } else if (kind === "final" || kind === "done") {
      if (text) stream.onEvent({ type: "message.delta", text });
      stream.onEvent({ type: "done" });
      this.finishStream(id);
    }
  }

  private finishStream(id: string, error?: Error) {
    const stream = this.streams.get(id);
    if (!stream) return;
    clearTimeout(stream.timeout);
    this.streams.delete(id);
    error ? stream.reject(error) : stream.resolve();
  }

  private emitAgents(raw: unknown) {
    const agents = listFrom<RecordValue>(raw, "agents").map(normalizeAgent).filter((agent) => agent.id);
    this.agentListeners.forEach((listener) => listener(agents));
  }

  private emitLog(level: LocalLogEntry["level"], message: string) {
    this.logListeners.forEach((listener) => listener(level, message));
  }

  private rejectAll(error: Error) {
    this.pending.forEach(({ reject, timeout }) => { clearTimeout(timeout); reject(error); });
    this.pending.clear();
    this.streams.forEach(({ reject, timeout }) => { clearTimeout(timeout); reject(error); });
    this.streams.clear();
  }

  private id(prefix: string) { this.sequence += 1; return `${prefix}_${Date.now()}_${this.sequence}`; }
}

function normalizeMessage(value: RecordValue): MessageInfo {
  const roleValue = String(value.role || "assistant");
  const role = roleValue === "user" || roleValue === "system" ? roleValue : roleValue === "thought" || roleValue === "reasoning" ? "reasoning" : "assistant";
  const rawContent = value.content;
  const content = Array.isArray(rawContent)
    ? rawContent.filter((block): block is RecordValue => Boolean(block && typeof block === "object")).filter((block) => block.type === "text").map((block) => ({ type: "text" as const, text: String(block.text || "") }))
    : [{ type: "text" as const, text: String(value.text || rawContent || "") }];
  return { id: String(value.id || "") || undefined, role, content, createdAt: value.created_at as string | number | undefined };
}
