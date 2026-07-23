export type AgentStatus = "idle" | "busy" | "disconnected" | "error" | "unknown";

export interface AgentInfo {
  id: string;
  displayName: string;
  status: AgentStatus;
}

// 授权模式
export type PermissionMode = "request_approval" | "auto_approve" | "full_access";

// 权限请求事件
export interface PermissionRequestEvent {
  session_id: string;
  agent_id: string;
  message?: string;
  tool_call?: unknown;
  params?: unknown;
  session_cwd?: string;
  permission_mode?: PermissionMode;
}

// 会话信息（扩展工作目录和授权模式）
export interface SessionInfo {
  id: string;
  agentId?: string;
  title?: string;
  cwd?: string;
  permission_mode?: PermissionMode;
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

export interface AgentStorageStats {
  agent_id: string;
  session_count: number;
  message_count: number;
}

export interface StorageInfo {
  store_dir: string;
  agent_count: number;
  total_sessions: number;
  total_messages: number;
  agents: AgentStorageStats[];
}

export type StreamEvent =
  | { type: "message.delta"; text: string }
  | { type: "reasoning.delta"; text: string }
  | { type: "session.updated"; sessionId: string }
  | { type: "done" }
  | { type: "error"; code: string; message: string };

// ─── 诊断数据类型 ──────────────────────────────────────────────────────────

export interface RuntimeInfo {
  name: string;
  command: string;
  found: boolean;
  path: string | null;
  version: string | null;
}

export interface ConfigDirInfo {
  path: string;
  exists: boolean;
}

export interface EnvKeyInfo {
  key: string;
  set: boolean;
}

export interface AgentDiagInfo {
  id: string;
  display: string;
  installed: boolean;
  acp_available: boolean;
  path: string | null;
  config_dirs: ConfigDirInfo[];
  env_keys: EnvKeyInfo[];
  bridge_status: string;
}

export interface NpmPkgInfo {
  name: string;
  version: string;
}

export interface DiagnosticsInfo {
  runtime: RuntimeInfo[];
  agents: AgentDiagInfo[];
  path: { count: number; has_node_modules: boolean };
  npm_global_agents: NpmPkgInfo[];
}
