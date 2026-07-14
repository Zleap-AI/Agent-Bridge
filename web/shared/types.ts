export type AgentStatus = "idle" | "busy" | "disconnected" | "error" | "unknown";

export interface AgentInfo {
  id: string;
  displayName: string;
  status: AgentStatus;
}

export interface SessionInfo {
  id: string;
  agentId?: string;
  messageCount?: number;
  createdAt?: string | number;
  updatedAt?: string | number;
}

export type MessageRole = "user" | "assistant" | "reasoning" | "system";

export interface TextContent {
  type: "text";
  text: string;
}

export interface MessageInfo {
  id?: string;
  role: MessageRole;
  content: TextContent[];
  createdAt?: string | number;
  pending?: boolean;
  error?: boolean;
}

export interface DeviceInfo {
  id: string;
  name: string;
  online: boolean;
  agentCount: number;
  lastSeenAt?: string | number;
}

export interface LocalRemoteStatus {
  paired: boolean;
  connected: boolean;
  state?: string;
  serverUrl: string;
  deviceId?: string;
  deviceName?: string;
  lastError?: string;
}

export interface LocalStatus {
  version: string;
  localAddress: string;
  healthy: boolean;
  agents: AgentInfo[];
  remote: LocalRemoteStatus;
}

export interface LocalLogEntry {
  timestamp: string | number;
  level: "debug" | "info" | "warn" | "error";
  message: string;
}

export interface PairingCodeInfo {
  code: string;
  expiresAt: string | number;
  consumed?: boolean;
}

export interface ApiKeyInfo {
  id: string;
  name: string;
  prefix: string;
  createdAt?: string | number;
  lastUsedAt?: string | number;
  key?: string;
}

export interface CallRecord {
  id?: string;
  deviceId: string;
  deviceName?: string;
  agentId: string;
  status: "success" | "error" | "running";
  durationMs?: number;
  createdAt: string | number;
  errorCode?: string;
}

export interface ServerStatus {
  initialized: boolean;
  version: string;
  healthy: boolean;
}

export type StreamEvent =
  | { type: "message.delta"; text: string }
  | { type: "reasoning.delta"; text: string }
  | { type: "session.updated"; sessionId: string }
  | { type: "done" }
  | { type: "error"; code: string; message: string };
