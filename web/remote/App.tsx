import {
  Activity,
  BookOpen,
  Bot,
  CheckCircle2,
  ChevronDown,
  CircleAlert,
  Clock3,
  ExternalLink,
  KeyRound,
  LogOut,
  Monitor,
  Network,
  Pencil,
  Plus,
  RefreshCw,
  Settings,
  ShieldCheck,
  Trash2,
  Unplug,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, isAbortError } from "../shared/api/http";
import { remoteApi } from "../shared/api/remote";
import { Conversation } from "../shared/components/Conversation";
import {
  Button,
  ConfirmDialog,
  CopyButton,
  Drawer,
  EmptyState,
  IconButton,
  LanguageControl,
  ListLink,
  Notice,
  Spinner,
  StatusDot,
  useMobileSidebar,
} from "../shared/components/ui";
import { formatDate, formatDuration, truncateMiddle } from "../shared/format";
import { useI18n } from "../shared/i18n";
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
} from "../shared/types";

type Phase = "loading" | "setup" | "login" | "console" | "error";
type DrawerName = "pairing" | "keys" | "calls" | "docs" | "settings" | null;

const maxDeviceNameLength = 120;
const maxAPIKeyNameLength = 100;

function codePointLength(value: string) {
  return Array.from(value).length;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function isUnauthorized(error: unknown) {
  return error instanceof ApiError && (error.status === 401 || error.code === "UNAUTHORIZED");
}

function expiresAtMilliseconds(value: string | number) {
  if (typeof value === "number") return value < 10_000_000_000 ? value * 1000 : value;
  const parsed = new Date(value).getTime();
  return Number.isNaN(parsed) ? Date.now() : parsed;
}

function AuthScreen({
  setup,
  server,
  onComplete,
}: {
  setup: boolean;
  server: ServerStatus | null;
  onComplete: () => void;
}) {
  const { t } = useI18n();
  const params = new URLSearchParams(window.location.search);
  const [token, setToken] = useState(params.get("token") || params.get("setup_token") || "");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const insecure = window.location.protocol === "http:";

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError("");
    if (setup && password !== confirmPassword) { setError(t("remote.passwordMismatch")); return; }
    if (setup && codePointLength(password) < 8) { setError(t("remote.passwordHint")); return; }
    if (!setup && !password) { setError(t("remote.passwordRequired")); return; }
    setLoading(true);
    try {
      if (setup) {
        if (!token.trim()) throw new Error(t("remote.setupTokenHint"));
        await remoteApi.setup(password, token.trim());
        await remoteApi.login(password);
        window.history.replaceState({}, "", window.location.pathname);
      } else await remoteApi.login(password);
      onComplete();
    } catch (submitError) { setError(errorMessage(submitError)); }
    finally { setLoading(false); }
  };

  return (
    <div className="auth-shell">
      <header className="auth-header">
        <div className="auth-brand"><div className="brand-mark"><Network size={17} aria-hidden="true" /></div><strong>Agent-Bridge Server</strong></div>
        <LanguageControl compact />
      </header>
      <main className="auth-main">
        <form className="auth-panel" onSubmit={submit}>
          <div className="auth-panel__icon">{setup ? <ShieldCheck size={20} aria-hidden="true" /> : <KeyRound size={20} aria-hidden="true" />}</div>
          <h1>{setup ? t("remote.setupTitle") : t("remote.loginTitle")}</h1>
          <p>{setup ? t("remote.setupBody") : t("remote.loginBody")}</p>
          <div className="form-stack">
            {insecure ? <Notice tone="warning">{t("remote.insecure")}</Notice> : null}
            {error ? <Notice tone="error">{error}</Notice> : null}
            {setup ? <label className="field"><span className="field__label">{t("remote.setupToken")}</span><input className="mono" type="password" value={token} onChange={(event) => setToken(event.target.value)} placeholder={t("remote.setupTokenHint")} autoComplete="one-time-code" autoFocus={!token} /></label> : null}
            <label className="field"><span className="field__label">{t("remote.password")}</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={t(setup ? "remote.passwordHint" : "remote.passwordRequired")} autoComplete={setup ? "new-password" : "current-password"} autoFocus={!setup || Boolean(token)} /></label>
            {setup ? <label className="field"><span className="field__label">{t("remote.passwordConfirm")}</span><input type="password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} autoComplete="new-password" /></label> : null}
            <Button variant="primary" loading={loading} type="submit">{setup ? t("remote.setupAction") : t("remote.loginAction")}</Button>
            {server ? <span className="field__hint">Agent-Bridge Server {server.version}</span> : null}
          </div>
        </form>
      </main>
    </div>
  );
}

export function RemoteApp() {
  const { t, locale } = useI18n();
  const [phase, setPhase] = useState<Phase>("loading");
  const [server, setServer] = useState<ServerStatus | null>(null);
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [selectedDeviceId, setSelectedDeviceId] = useState("");
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [sessionId, setSessionId] = useState("");
  const [messages, setMessages] = useState<MessageInfo[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [messagesLoading, setMessagesLoading] = useState(false);
  const [sending, setSending] = useState(false);
  const [drawer, setDrawer] = useState<DrawerName>(null);
  const [mobileMenu, setMobileMenu] = useState(false);
  const [workspaceError, setWorkspaceError] = useState("");
  const [devicePollError, setDevicePollError] = useState("");
  const [agentPollError, setAgentPollError] = useState("");
  const [pairing, setPairing] = useState<PairingCodeInfo | null>(null);
  const [pairingLoading, setPairingLoading] = useState(false);
  const [pairingError, setPairingError] = useState("");
  const [now, setNow] = useState(Date.now());
  const [keys, setKeys] = useState<ApiKeyInfo[]>([]);
  const [keyName, setKeyName] = useState("");
  const [createdKey, setCreatedKey] = useState<ApiKeyInfo | null>(null);
  const [keysLoading, setKeysLoading] = useState(false);
  const [keyError, setKeyError] = useState("");
  const [deleteKey, setDeleteKey] = useState<ApiKeyInfo | null>(null);
  const [calls, setCalls] = useState<CallRecord[]>([]);
  const [callsLoading, setCallsLoading] = useState(false);
  const [callsError, setCallsError] = useState("");
  const [deviceName, setDeviceName] = useState("");
  const [savingDevice, setSavingDevice] = useState(false);
  const [deviceSettingsError, setDeviceSettingsError] = useState("");
  const [confirmDeleteDevice, setConfirmDeleteDevice] = useState(false);
  const deviceLoadGeneration = useRef(0);
  const sessionLoadGeneration = useRef(0);
  const messageLoadGeneration = useRef(0);
  const streamGeneration = useRef(0);
  const agentsDeviceIdRef = useRef("");
  const workspaceContextRef = useRef("");
  const sessionIdRef = useRef("");
  const skipMessageLoadForSession = useRef("");
  const sessionAbortRef = useRef<AbortController | null>(null);
  const messageAbortRef = useRef<AbortController | null>(null);
  const closeMobileMenu = useCallback(() => setMobileMenu(false), []);
  const mobileSidebarRef = useMobileSidebar(mobileMenu, closeMobileMenu);

  workspaceContextRef.current = `${selectedDeviceId}\u0000${selectedAgentId}`;

  const selectedDevice = devices.find((device) => device.id === selectedDeviceId) || null;
  const selectedAgent = agents.find((agent) => agent.id === selectedAgentId) || null;

  const handleApiError = useCallback((error: unknown) => {
    if (isUnauthorized(error)) {
      sessionAbortRef.current?.abort();
      messageAbortRef.current?.abort();
      setPhase("login");
      setDrawer(null);
      return true;
    }
    return false;
  }, []);

  const loadDevices = useCallback(async (signal?: AbortSignal) => {
    const generation = ++deviceLoadGeneration.current;
    try {
      const next = await remoteApi.getDevices(signal);
      if (generation !== deviceLoadGeneration.current) return false;
      setDevicePollError("");
      setDevices(next);
      setSelectedDeviceId((current) => next.some((device) => device.id === current) ? current : next[0]?.id || "");
      return true;
    } catch (error) {
      if (generation !== deviceLoadGeneration.current || signal?.aborted || isAbortError(error)) return false;
      if (!handleApiError(error)) setDevicePollError(errorMessage(error));
      return false;
    }
  }, [handleApiError]);

  const initialize = useCallback(async () => {
    setPhase("loading");
    try {
      const nextStatus = await remoteApi.getStatus();
      setServer(nextStatus);
      if (!nextStatus.initialized) { setPhase("setup"); return; }
      const generation = ++deviceLoadGeneration.current;
      try {
        const next = await remoteApi.getDevices();
        if (generation !== deviceLoadGeneration.current) return;
        setDevices(next);
        setSelectedDeviceId(next[0]?.id || "");
        setPhase("console");
      } catch (error) {
        if (generation !== deviceLoadGeneration.current) return;
        if (isUnauthorized(error)) setPhase("login");
        else throw error;
      }
    } catch (error) {
      setWorkspaceError(errorMessage(error));
      setPhase("error");
    }
  }, []);

  useEffect(() => { void initialize(); }, [initialize]);
  useEffect(() => {
    if (phase !== "console") return;
    let controller: AbortController | null = null;
    const refresh = () => {
      controller?.abort();
      controller = new AbortController();
      void loadDevices(controller.signal);
    };
    const timer = window.setInterval(refresh, 5000);
    return () => { window.clearInterval(timer); controller?.abort(); };
  }, [phase, loadDevices]);
  useEffect(() => {
    if (!pairing) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [pairing]);

  useEffect(() => {
    sessionAbortRef.current?.abort();
    messageAbortRef.current?.abort();
    sessionLoadGeneration.current += 1;
    messageLoadGeneration.current += 1;
    agentsDeviceIdRef.current = "";
    setAgentPollError("");
    setAgents([]); setSelectedAgentId(""); setSessions([]); setSessionId(""); setMessages([]);
    if (!selectedDeviceId || phase !== "console") return;
    setWorkspaceError("");
    let active = true;
    let requestGeneration = 0;
    let controller: AbortController | null = null;
    const refresh = async () => {
      const generation = ++requestGeneration;
      controller?.abort();
      controller = new AbortController();
      try {
        const next = await remoteApi.getAgents(selectedDeviceId, controller.signal);
        if (!active || generation !== requestGeneration) return;
        agentsDeviceIdRef.current = selectedDeviceId;
        setAgentPollError("");
        setAgents(next);
        setSelectedAgentId((current) => next.some((agent) => agent.id === current) ? current : next[0]?.id || "");
      } catch (error) {
        if (!active || generation !== requestGeneration || controller.signal.aborted || isAbortError(error)) return;
        if (!handleApiError(error)) setAgentPollError(errorMessage(error));
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => { active = false; requestGeneration += 1; controller?.abort(); window.clearInterval(timer); };
  }, [handleApiError, phase, selectedDeviceId]);

  useEffect(() => {
    setDeviceName(selectedDevice?.name || "");
    setDeviceSettingsError("");
  }, [selectedDeviceId, selectedDevice?.name]);

  const loadSessions = useCallback(async (agentId: string, preferred = "") => {
    sessionAbortRef.current?.abort();
    const deviceId = selectedDeviceId;
    const contextKey = `${deviceId}\u0000${agentId}`;
    const generation = ++sessionLoadGeneration.current;
    if (!deviceId || !agentId || !selectedDevice?.online || agentsDeviceIdRef.current !== deviceId) {
      setSessions([]); setSessionId(""); setMessages([]); setSessionsLoading(false);
      return;
    }
    const controller = new AbortController();
    sessionAbortRef.current = controller;
    setSessionsLoading(true); setWorkspaceError("");
    try {
      const next = await remoteApi.getSessions(deviceId, agentId, controller.signal);
      if (generation !== sessionLoadGeneration.current || workspaceContextRef.current !== contextKey) return;
      setSessions(next);
      setSessionId(next.some((session) => session.id === preferred) ? preferred : next[0]?.id || "");
    } catch (error) {
      if (controller.signal.aborted || isAbortError(error)) return;
      if (generation === sessionLoadGeneration.current && workspaceContextRef.current === contextKey && !handleApiError(error)) {
        setWorkspaceError(errorMessage(error));
      }
    } finally {
      if (sessionAbortRef.current === controller) sessionAbortRef.current = null;
      if (generation === sessionLoadGeneration.current && workspaceContextRef.current === contextKey) setSessionsLoading(false);
    }
  }, [handleApiError, selectedDevice?.online, selectedDeviceId]);

  useEffect(() => { void loadSessions(selectedAgentId); }, [selectedAgentId, selectedDevice?.online, loadSessions]);

  useEffect(() => {
    streamGeneration.current += 1;
    setSending(false);
  }, [selectedAgentId, selectedDeviceId]);

  useEffect(() => {
    sessionIdRef.current = sessionId;
    const generation = ++messageLoadGeneration.current;
    messageAbortRef.current?.abort();
    if (skipMessageLoadForSession.current === sessionId) {
      skipMessageLoadForSession.current = "";
      setMessagesLoading(false);
      return;
    }
    setMessages([]);
    if (!selectedDeviceId || !selectedAgentId || !sessionId || !selectedDevice?.online) {
      setMessagesLoading(false);
      return;
    }
    const controller = new AbortController();
    messageAbortRef.current = controller;
    setMessagesLoading(true); setWorkspaceError("");
    void remoteApi.getMessages(selectedDeviceId, selectedAgentId, sessionId, controller.signal)
      .then((next) => { if (generation === messageLoadGeneration.current) setMessages(next); })
      .catch((error) => {
        if (controller.signal.aborted || isAbortError(error)) return;
        if (generation === messageLoadGeneration.current && !handleApiError(error)) setWorkspaceError(errorMessage(error));
      })
      .finally(() => {
        if (messageAbortRef.current === controller) messageAbortRef.current = null;
        if (generation === messageLoadGeneration.current) setMessagesLoading(false);
      });
    return () => controller.abort();
  }, [selectedDeviceId, selectedAgentId, sessionId, selectedDevice?.online, handleApiError]);

  const createSession = async () => {
    if (!selectedDeviceId || !selectedAgentId) return;
    const deviceId = selectedDeviceId;
    const agentId = selectedAgentId;
    const contextKey = `${deviceId}\u0000${agentId}`;
    sessionAbortRef.current?.abort();
    const generation = ++sessionLoadGeneration.current;
    setSessionsLoading(true); setWorkspaceError("");
    try {
      const created = await remoteApi.createSession(deviceId, agentId);
      if (generation !== sessionLoadGeneration.current || workspaceContextRef.current !== contextKey) return;
      setSessions((current) => [created, ...current.filter((session) => session.id !== created.id)]);
      setSessionId(created.id); setMessages([]);
    } catch (error) {
      if (generation === sessionLoadGeneration.current && workspaceContextRef.current === contextKey && !handleApiError(error)) {
        setWorkspaceError(`${t("session.createFailed")}: ${errorMessage(error)}`);
      }
    } finally {
      if (generation === sessionLoadGeneration.current && workspaceContextRef.current === contextKey) setSessionsLoading(false);
    }
  };

  const updateStream = (assistantId: string, reasoningId: string, event: StreamEvent, generation: number, contextKey: string, agentId: string) => {
    if (generation !== streamGeneration.current || workspaceContextRef.current !== contextKey) return;
    if (event.type === "session.updated") {
      if (event.sessionId && event.sessionId !== sessionIdRef.current) {
        messageLoadGeneration.current += 1;
        sessionIdRef.current = event.sessionId;
        skipMessageLoadForSession.current = event.sessionId;
        setSessionId(event.sessionId);
        setSessions((current) => [{ id: event.sessionId, agentId }, ...current.filter((session) => session.id !== event.sessionId)]);
      }
      return;
    }
    setMessages((current) => {
      if (event.type === "reasoning.delta") {
        const exists = current.some((message) => message.id === reasoningId);
        if (!exists) {
          const copy = [...current];
          const index = copy.findIndex((message) => message.id === assistantId);
          copy.splice(Math.max(0, index), 0, { id: reasoningId, role: "reasoning", content: [{ type: "text", text: event.text }] });
          return copy;
        }
        return current.map((message) => message.id === reasoningId ? { ...message, content: [{ type: "text", text: `${message.content[0]?.text || ""}${event.text}` }] } : message);
      }
      if (event.type === "message.delta") return current.map((message) => message.id === assistantId ? { ...message, pending: true, content: [{ type: "text", text: `${message.content[0]?.text || ""}${event.text}` }] } : message);
      if (event.type === "error") return current.map((message) => message.id === assistantId ? { ...message, pending: false, error: true, content: [{ type: "text", text: event.message }] } : message);
      if (event.type === "done") return current.map((message) => message.id === assistantId ? { ...message, pending: false } : message);
      return current;
    });
  };

  const sendMessage = async (text: string) => {
    if (!selectedDeviceId || !selectedAgentId || !sessionId) return;
    const deviceId = selectedDeviceId;
    const agentId = selectedAgentId;
    const activeSessionId = sessionId;
    const contextKey = `${deviceId}\u0000${agentId}`;
    const generation = ++streamGeneration.current;
    const stamp = String(Date.now());
    const assistantId = `assistant-${stamp}`;
    const reasoningId = `reasoning-${stamp}`;
    setMessages((current) => [
      ...current,
      { id: `user-${stamp}`, role: "user", content: [{ type: "text", text }] },
      { id: assistantId, role: "assistant", content: [{ type: "text", text: "" }], pending: true },
    ]);
    setSending(true); setWorkspaceError("");
    try { await remoteApi.streamMessage(deviceId, agentId, activeSessionId, text, (event) => updateStream(assistantId, reasoningId, event, generation, contextKey, agentId)); }
    catch (error) {
      if (generation !== streamGeneration.current || workspaceContextRef.current !== contextKey) return;
      if (!handleApiError(error)) {
        setMessages((current) => current.map((message) => message.id === assistantId ? { ...message, pending: false, error: true, content: [{ type: "text", text: errorMessage(error) }] } : message));
      }
      throw error;
    } finally {
      if (generation === streamGeneration.current && workspaceContextRef.current === contextKey) setSending(false);
    }
  };

  const openPairing = () => { setDrawer("pairing"); setPairingError(""); setMobileMenu(false); };
  const generatePairing = async () => {
    setPairingLoading(true); setPairingError("");
    try { setPairing(await remoteApi.createPairingCode()); setNow(Date.now()); }
    catch (error) { if (!handleApiError(error)) setPairingError(errorMessage(error)); }
    finally { setPairingLoading(false); }
  };

  const loadKeys = async () => {
    setDrawer("keys"); setMobileMenu(false); setKeysLoading(true); setKeyError("");
    try { setKeys(await remoteApi.getApiKeys()); }
    catch (error) { if (!handleApiError(error)) setKeyError(errorMessage(error)); }
    finally { setKeysLoading(false); }
  };
  const createKey = async () => {
    const name = keyName.trim();
    if (!name) return;
    if (codePointLength(name) > maxAPIKeyNameLength) { setKeyError(t("remote.keyNameTooLong")); return; }
    setKeysLoading(true); setKeyError("");
    try {
      const created = await remoteApi.createApiKey(name);
      setCreatedKey(created); setKeys((current) => [created, ...current]); setKeyName("");
    } catch (error) { if (!handleApiError(error)) setKeyError(errorMessage(error)); }
    finally { setKeysLoading(false); }
  };
  const revokeKey = async () => {
    if (!deleteKey) return;
    setKeysLoading(true);
    try { await remoteApi.deleteApiKey(deleteKey.id); setKeys((current) => current.filter((key) => key.id !== deleteKey.id)); setDeleteKey(null); }
    catch (error) { if (!handleApiError(error)) setKeyError(errorMessage(error)); }
    finally { setKeysLoading(false); }
  };

  const loadCalls = async () => {
    setDrawer("calls"); closeMobileMenu(); setCallsLoading(true); setCallsError("");
    try { setCalls(await remoteApi.getCalls()); }
    catch (error) { if (!handleApiError(error)) setCallsError(errorMessage(error)); }
    finally { setCallsLoading(false); }
  };

  const openSettings = () => {
    setDeviceSettingsError("");
    setDrawer("settings");
    closeMobileMenu();
  };

  const saveDeviceName = async () => {
    const name = deviceName.trim();
    if (!selectedDevice || !name) return;
    if (codePointLength(name) > maxDeviceNameLength) { setDeviceSettingsError(t("remote.deviceNameTooLong")); return; }
    setSavingDevice(true); setDeviceSettingsError("");
    try {
      const updated = await remoteApi.renameDevice(selectedDevice.id, name);
      setDevices((current) => current.map((device) => device.id === updated.id ? { ...device, name: updated.name } : device));
    } catch (error) { if (!handleApiError(error)) setDeviceSettingsError(errorMessage(error)); }
    finally { setSavingDevice(false); }
  };
  const deleteDevice = async () => {
    if (!selectedDevice) return;
    setSavingDevice(true); setDeviceSettingsError("");
    try {
      await remoteApi.deleteDevice(selectedDevice.id);
      setConfirmDeleteDevice(false); setDrawer(null); setSelectedDeviceId(""); await loadDevices();
    } catch (error) {
      if (!handleApiError(error)) {
        setConfirmDeleteDevice(false);
        setDeviceSettingsError(errorMessage(error));
      }
    }
    finally { setSavingDevice(false); }
  };
  const logout = async () => {
    try { await remoteApi.logout(); } catch { /* session may already be gone */ }
    setPhase("login"); setDrawer(null); setDevices([]);
  };

  if (phase === "loading") return <div className="page-loading"><Spinner /></div>;
  if (phase === "error") return <div className="auth-shell"><header className="auth-header"><div className="auth-brand"><div className="brand-mark"><Network size={17} aria-hidden="true" /></div><strong>Agent-Bridge Server</strong></div><LanguageControl compact /></header><main className="auth-main"><EmptyState compact icon={CircleAlert} title={t("common.error")} body={workspaceError} action={<Button variant="primary" icon={RefreshCw} onClick={() => void initialize()}>{t("common.retry")}</Button>} /></main></div>;
  if (phase === "setup" || phase === "login") return <AuthScreen setup={phase === "setup"} server={server} onComplete={() => { setPhase("console"); void loadDevices(); }} />;

  const insecure = window.location.protocol === "http:";
  const remaining = pairing ? Math.max(0, expiresAtMilliseconds(pairing.expiresAt) - now) : 0;
  const pairingExpired = Boolean(pairing && remaining <= 0);
  const enabled = Boolean(selectedDevice?.online);
  const navigationLocked = sending;
  const visibleWorkspaceError = workspaceError || devicePollError || agentPollError;

  return (
    <div className="app-shell">
      {mobileMenu ? <button className="sidebar-backdrop" aria-label={t("common.close")} onClick={closeMobileMenu} tabIndex={-1} /> : null}
      <aside id="app-navigation" ref={mobileSidebarRef} className={`sidebar ${mobileMenu ? "is-open" : ""}`} tabIndex={-1}>
        <div className="sidebar__brand">
          <div className="brand-mark"><Network size={17} aria-hidden="true" /></div>
          <div className="sidebar__brand-copy"><strong>Agent-Bridge</strong><span>Remote Console</span></div>
        </div>
        <section className="sidebar__section">
          <div className="sidebar__section-title"><span>{t("remote.devices")}</span><span>{devices.length}</span></div>
          <div className="sidebar__list">
            {devices.length ? devices.map((device) => (
              <button
                className={`sidebar__item ${device.id === selectedDeviceId ? "is-active" : ""}`}
                key={device.id}
                disabled={navigationLocked}
                onClick={() => {
                  sessionLoadGeneration.current += 1;
                  messageLoadGeneration.current += 1;
                  setSelectedDeviceId(device.id);
                  closeMobileMenu();
                }}
              >
                <span className="sidebar__item-icon"><Monitor size={16} aria-hidden="true" /></span>
                <span className="sidebar__item-copy"><strong title={device.name}>{device.name}</strong><span title={`${device.online ? t("common.online") : t("common.offline")}${device.lastSeenAt ? ` · ${t("remote.lastSeen")} ${formatDate(device.lastSeenAt, locale)}` : ""}`}><StatusDot status={device.online ? "online" : "offline"} /><span className="sidebar__item-status-text">{device.online ? t("common.online") : t("common.offline")}{device.lastSeenAt ? ` · ${t("remote.lastSeen")} ${formatDate(device.lastSeenAt, locale)}` : ""}</span></span></span>
                <span className="sidebar__item-meta" title={`${device.agentCount} ${t("remote.agents")}`}>{device.agentCount}</span>
              </button>
            )) : <div className="sidebar__empty">{t("remote.noDevicesBody")}</div>}
          </div>
        </section>
        <nav className="sidebar__footer">
          <IconButton icon={Plus} label={t("remote.pairing")} active={drawer === "pairing"} onClick={openPairing} />
          <IconButton icon={KeyRound} label={t("remote.apiKeys")} active={drawer === "keys"} onClick={() => void loadKeys()} />
          <IconButton icon={Activity} label={t("remote.calls")} active={drawer === "calls"} onClick={() => void loadCalls()} />
          <IconButton icon={BookOpen} label={t("remote.docs")} active={drawer === "docs"} onClick={() => { setDrawer("docs"); setMobileMenu(false); }} />
          <IconButton icon={Settings} label={t("common.settings")} active={drawer === "settings"} onClick={openSettings} />
        </nav>
      </aside>

      <div className="shell-column" inert={mobileMenu ? true : undefined}>
        {insecure ? <div className="top-warning">{t("remote.insecure")}</div> : null}
        {visibleWorkspaceError ? <div className="top-warning top-warning--error">{visibleWorkspaceError}</div> : null}
        {devices.length === 0 ? (
          <main className="workspace">
            <header className="workspace__header"><div className="workspace__identity"><IconButton icon={Network} label={t("common.mobileMenu")} onClick={() => setMobileMenu(true)} className="mobile-menu-button" aria-expanded={mobileMenu} aria-controls="app-navigation" /><div className="workspace__title"><strong>{t("remote.console")}</strong><span>Agent-Bridge Server {server?.version}</span></div></div></header>
            <EmptyState icon={Monitor} title={t("remote.noDevicesTitle")} body={t("remote.noDevicesBody")} action={<Button variant="primary" icon={Plus} onClick={openPairing}>{t("remote.generateCode")}</Button>} />
          </main>
        ) : (
          <Conversation
            agent={selectedAgent}
            contextLabel={selectedDevice ? `${selectedDevice.name} · ${selectedDevice.online ? t("common.online") : t("common.offline")}` : undefined}
            agentControl={<div className="agent-control"><select value={selectedAgentId} onChange={(event) => { sessionLoadGeneration.current += 1; messageLoadGeneration.current += 1; setSessions([]); setSessionId(""); setMessages([]); setSelectedAgentId(event.target.value); }} disabled={!agents.length || navigationLocked} aria-label={t("remote.agents")}><option value="">{t("remote.chooseAgent")}</option>{agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.displayName}</option>)}</select><ChevronDown size={15} aria-hidden="true" /></div>}
            sessions={sessions}
            sessionId={sessionId}
            onSelectSession={setSessionId}
            onCreateSession={createSession}
            onRefreshSessions={() => loadSessions(selectedAgentId, sessionId)}
            sessionsLoading={sessionsLoading}
            messages={messages}
            messagesLoading={messagesLoading}
            sending={sending}
            enabled={enabled}
            unavailableTitle={t("remote.deviceOffline")}
            unavailableBody={t("remote.deviceOfflineBody")}
            onSend={sendMessage}
            onOpenMobileMenu={() => setMobileMenu(true)}
            mobileMenuOpen={mobileMenu}
          />
        )}
      </div>

      <Drawer open={drawer === "pairing"} title={t("remote.pairing")} description={t("remote.pairingBody")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          {pairingError ? <Notice tone="error">{pairingError}</Notice> : null}
          {!pairing ? <EmptyState compact icon={Unplug} title={t("remote.pairing")} body={t("remote.pairingBody")} action={<Button variant="primary" icon={Plus} onClick={() => void generatePairing()} loading={pairingLoading}>{t("remote.generateCode")}</Button>} /> : (
            <>
              <div className={`pairing-code ${pairingExpired ? "is-expired" : ""}`}>
                <span className="pairing-code__value">{pairing.code}</span>
                <span className="pairing-code__timer">{pairingExpired ? t("remote.codeExpired") : `${t("remote.codeExpires")} ${String(Math.floor(remaining / 60_000)).padStart(2, "0")}:${String(Math.floor((remaining % 60_000) / 1000)).padStart(2, "0")}`}</span>
              </div>
              {!pairingExpired ? <CopyButton value={pairing.code} /> : null}
              <Button variant="secondary" icon={RefreshCw} onClick={() => void generatePairing()} loading={pairingLoading}>{t("remote.generateAgain")}</Button>
            </>
          )}
        </div>
      </Drawer>

      <Drawer open={drawer === "keys"} title={t("remote.apiKeys")} description={t("remote.apiKeysBody")} onClose={() => { setDrawer(null); setCreatedKey(null); }}>
        <div className="form-stack">
          {keyError ? <Notice tone="error">{keyError}</Notice> : null}
          {createdKey?.key ? <><Notice tone="warning">{t("remote.keyShownOnce")}</Notice><div className="secret-display">{createdKey.key}</div><CopyButton value={createdKey.key} /></> : null}
          <div className="section-heading"><div><h3>{t("remote.newApiKey")}</h3></div></div>
          <label className="field"><span className="field__label">{t("remote.keyName")}</span><input value={keyName} onChange={(event) => setKeyName(event.target.value)} placeholder={t("remote.keyNameHint")} /><span className="field__hint">{t("remote.keyNameLimit")}</span></label>
          <Button variant="primary" icon={Plus} onClick={() => void createKey()} loading={keysLoading} disabled={!keyName.trim()}>{t("common.create")}</Button>
          <div className="section-heading"><div><h3>{t("remote.apiKeys")}</h3></div></div>
          {keysLoading && !keys.length ? <Spinner /> : keys.length ? <div className="data-list">{keys.map((key) => (
            <div className="data-row" key={key.id}>
              <div className="data-row__copy"><strong>{key.name}</strong><span className="mono">{key.prefix}•••• · {t("remote.lastUsed")} {formatDate(key.lastUsedAt, locale, t("common.never"))}</span></div>
              <div className="data-row__actions"><IconButton icon={Trash2} label={t("common.delete")} danger onClick={() => setDeleteKey(key)} /></div>
            </div>
          ))}</div> : <Notice>{t("remote.keyEmpty")}</Notice>}
        </div>
      </Drawer>

      <Drawer open={drawer === "calls"} title={t("remote.calls")} description={t("remote.callsBody")} onClose={() => setDrawer(null)} wide>
        {callsError ? <Notice tone="error">{callsError}</Notice> : callsLoading ? <Spinner /> : calls.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>{t("remote.createdAt")}</th><th>Device</th><th>Agent</th><th>{t("common.status")}</th><th>{t("remote.duration")}</th></tr></thead><tbody>{calls.map((call, index) => <tr key={call.id || index}><td>{formatDate(call.createdAt, locale)}</td><td>{devices.find((device) => device.id === call.deviceId)?.name || call.deviceName || truncateMiddle(call.deviceId, 18)}</td><td>{call.agentId}</td><td><span className={`status-text status-text--${call.status}`}>{call.status === "success" ? <CheckCircle2 size={13} /> : call.status === "running" ? <Clock3 size={13} /> : <CircleAlert size={13} />}{t(`remote.${call.status === "error" ? "failed" : call.status}`)}</span></td><td>{formatDuration(call.durationMs)}</td></tr>)}</tbody></table></div> : <EmptyState compact icon={Activity} title={t("remote.calls")} body={t("remote.callsEmpty")} />}
      </Drawer>

      <Drawer open={drawer === "docs"} title={t("remote.docs")} description={t("remote.docsBody")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          <Notice>{t("remote.apiAuth")}</Notice>
          <div className="section-heading"><div><h3>{t("remote.resources")}</h3></div></div>
          <div className="endpoint-list">
            <div className="endpoint"><strong>GET</strong><code>/api/v1/devices</code></div>
            <div className="endpoint"><strong>GET</strong><code>/api/v1/devices/:device/agents</code></div>
            <div className="endpoint"><strong>POST</strong><code>/api/v1/devices/:device/agents/:agent/sessions</code></div>
            <div className="endpoint"><strong>POST</strong><code>/api/v1/devices/:device/agents/:agent/sessions/:session/messages</code></div>
          </div>
          <div className="section-heading"><div><h3>{t("remote.messageFormat")}</h3></div></div>
          <pre className="code-block">{`{
  "content": [
    {"type": "text", "text": "Hello"}
  ]
}`}</pre>
          <div className="section-heading"><div><h3>{t("remote.streamEvents")}</h3></div></div>
          <div className="event-tags"><span className="event-tag">message.delta</span><span className="event-tag">reasoning.delta</span><span className="event-tag">session.updated</span><span className="event-tag">done</span><span className="event-tag">error</span></div>
          <div className="data-list">
            <ListLink icon={BookOpen} title={t("remote.openDocs")} body="/docs" onClick={() => window.open("/docs", "_blank", "noopener")} />
            <ListLink icon={ExternalLink} title={t("remote.downloadOpenApi")} body="/openapi.json" onClick={() => window.open("/openapi.json", "_blank", "noopener")} />
          </div>
        </div>
      </Drawer>

      <Drawer open={drawer === "settings"} title={t("remote.settings")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          {deviceSettingsError ? <Notice tone="error">{deviceSettingsError}</Notice> : null}
          <LanguageControl />
          <div className="settings-list data-list">
            <div className="data-row"><div className="data-row__copy"><strong>{t("common.version")}</strong><span>{server?.version || "0.4.0"}</span></div></div>
            {selectedDevice ? <><div className="section-heading"><div><h3>Device</h3><p className="mono">{truncateMiddle(selectedDevice.id, 36)}</p></div></div><label className="field"><span className="field__label">{t("common.name")}</span><input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} /><span className="field__hint">{t("remote.deviceNameLimit")}</span></label><Button variant="secondary" icon={Pencil} loading={savingDevice} disabled={!deviceName.trim() || deviceName.trim() === selectedDevice.name} onClick={() => void saveDeviceName()}>{t("common.save")}</Button><Button variant="ghost" icon={Trash2} onClick={() => setConfirmDeleteDevice(true)}>{t("remote.deleteDevice")}</Button></> : null}
          </div>
          <Button variant="secondary" icon={LogOut} onClick={() => void logout()}>{t("common.logout")}</Button>
        </div>
      </Drawer>

      <ConfirmDialog open={Boolean(deleteKey)} title={t("remote.revokeKey")} body={t("remote.revokeKeyBody")} confirmLabel={t("common.delete")} danger loading={keysLoading} onCancel={() => setDeleteKey(null)} onConfirm={() => void revokeKey()} />
      <ConfirmDialog open={confirmDeleteDevice} title={t("remote.deleteDeviceTitle")} body={t("remote.deleteDeviceBody")} confirmLabel={t("remote.deleteDevice")} danger loading={savingDevice} onCancel={() => setConfirmDeleteDevice(false)} onConfirm={() => void deleteDevice()} />
    </div>
  );
}
