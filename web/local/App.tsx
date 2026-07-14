import {
  Bot,
  Cable,
  CircleAlert,
  Network,
  RefreshCw,
  ScrollText,
  Settings,
  Unplug,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalAdminClient, localApi, type LocalSettings } from "../shared/api/local";
import { ApiError } from "../shared/api/http";
import { Conversation } from "../shared/components/Conversation";
import {
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  IconButton,
  LanguageControl,
  Notice,
  Spinner,
  StatusDot,
  useMobileSidebar,
} from "../shared/components/ui";
import { formatDate, truncateMiddle } from "../shared/format";
import { useI18n } from "../shared/i18n";
import type { AgentInfo, LocalLogEntry, LocalStatus, MessageInfo, SessionInfo, StreamEvent } from "../shared/types";

type DrawerName = "remote" | "logs" | "settings" | "session" | null;

const initialStatus: LocalStatus = {
  version: "0.4.0",
  localAddress: "127.0.0.1:9202",
  healthy: false,
  agents: [],
  remote: { paired: false, connected: false, serverUrl: "" },
};

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function connectionStatus(status: AgentInfo["status"]): "online" | "busy" | "offline" | "error" {
  if (status === "idle") return "online";
  if (status === "busy") return "busy";
  if (status === "error") return "error";
  return "offline";
}

export function LocalApp() {
  const { t, locale } = useI18n();
  const client = useMemo(() => new LocalAdminClient(), []);
  const [initializing, setInitializing] = useState(true);
  const [status, setStatus] = useState<LocalStatus>(initialStatus);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [sessionId, setSessionId] = useState("");
  const [messages, setMessages] = useState<MessageInfo[]>([]);
  const [wsConnected, setWsConnected] = useState(false);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [existingSessionId, setExistingSessionId] = useState("");
  const [existingSessionLoading, setExistingSessionLoading] = useState(false);
  const [existingSessionError, setExistingSessionError] = useState("");
  const [messagesLoading, setMessagesLoading] = useState(false);
  const [sending, setSending] = useState(false);
  const [drawer, setDrawer] = useState<DrawerName>(null);
  const [mobileMenu, setMobileMenu] = useState(false);
  const [logs, setLogs] = useState<LocalLogEntry[]>([]);
  const [serverUrl, setServerUrl] = useState("");
  const [pairingCode, setPairingCode] = useState("");
  const [pairing, setPairing] = useState(false);
  const [pairError, setPairError] = useState("");
  const [pairSuccess, setPairSuccess] = useState(false);
  const [confirmReplace, setConfirmReplace] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);
  const [unpairing, setUnpairing] = useState(false);
  const [workspaceError, setWorkspaceError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [localSettings, setLocalSettings] = useState<LocalSettings>({ debug: false, claudeSettingsFile: "", restartRequired: false });
  const [settingsLoading, setSettingsLoading] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const sessionLoadGeneration = useRef(0);
  const messageLoadGeneration = useRef(0);
  const streamGeneration = useRef(0);
  const selectedAgentIdRef = useRef("");
  const sessionIdRef = useRef("");
  const skipMessageLoadForSession = useRef("");
  const closeMobileMenu = useCallback(() => setMobileMenu(false), []);
  const mobileSidebarRef = useMobileSidebar(mobileMenu, closeMobileMenu);

  selectedAgentIdRef.current = selectedAgentId;

  const selectedAgent = agents.find((agent) => agent.id === selectedAgentId) || null;

  const refreshStatus = useCallback(async () => {
    try {
      const next = await localApi.getStatus();
      setStatusError("");
      setStatus(next);
      setAgents(next.agents);
      setSelectedAgentId((current) => next.agents.some((agent) => agent.id === current) ? current : next.agents[0]?.id || "");
      if (next.remote.serverUrl) setServerUrl((current) => current || next.remote.serverUrl);
      return true;
    } catch (error) {
      setStatus((current) => ({ ...current, healthy: false }));
      setStatusError(errorMessage(error));
      return false;
    }
  }, []);

  useEffect(() => {
    const offConnection = client.onConnection(setWsConnected);
    const offAgents = client.onAgents((next) => {
      setAgents(next);
      setSelectedAgentId((current) => next.some((agent) => agent.id === current) ? current : next[0]?.id || "");
    });
    const offLog = client.onLog((level, message) => setLogs((current) => [
      ...current.slice(-199),
      { timestamp: new Date().toISOString(), level, message },
    ]));
    client.connect();
    void Promise.allSettled([refreshStatus(), localApi.getAgents().then((next) => {
      setAgents(next);
      setSelectedAgentId((current) => current || next[0]?.id || "");
    })]).finally(() => setInitializing(false));
    const timer = window.setInterval(() => void refreshStatus(), 5000);
    return () => {
      window.clearInterval(timer);
      offConnection(); offAgents(); offLog(); client.close();
    };
  }, [client, refreshStatus]);

  const loadSessions = useCallback(async (agentId: string, preferred = "") => {
    const generation = ++sessionLoadGeneration.current;
    if (!agentId || !client.connected) {
      setSessions([]); setSessionId(""); setMessages([]); setSessionsLoading(false);
      return;
    }
    setSessionsLoading(true);
    setWorkspaceError("");
    try {
      const next = await client.listSessions(agentId);
      if (generation !== sessionLoadGeneration.current || selectedAgentIdRef.current !== agentId) return;
      const unique = Array.from(new Map(next.map((session) => [session.id, session])).values());
      setSessions(unique);
      const nextId = unique.some((session) => session.id === preferred) ? preferred : unique[0]?.id || "";
      setSessionId(nextId);
    } catch (error) {
      if (generation !== sessionLoadGeneration.current || selectedAgentIdRef.current !== agentId) return;
      setSessions([]);
      setSessionId("");
      setWorkspaceError(errorMessage(error));
    } finally {
      if (generation === sessionLoadGeneration.current && selectedAgentIdRef.current === agentId) setSessionsLoading(false);
    }
  }, [client]);

  useEffect(() => { void loadSessions(selectedAgentId); }, [selectedAgentId, wsConnected, loadSessions]);

  useEffect(() => {
    streamGeneration.current += 1;
    setSending(false);
  }, [selectedAgentId]);

  const loadMessages = useCallback(async (nextId: string) => {
    const generation = ++messageLoadGeneration.current;
    setMessages([]);
    if (!nextId || !selectedAgentId) return;
    setMessagesLoading(true);
    setWorkspaceError("");
    try {
      const next = await client.getMessages(selectedAgentId, nextId);
      if (generation === messageLoadGeneration.current) setMessages(next);
    } catch (error) {
      if (generation === messageLoadGeneration.current) setWorkspaceError(errorMessage(error));
    } finally {
      if (generation === messageLoadGeneration.current) setMessagesLoading(false);
    }
  }, [client, selectedAgentId]);

  useEffect(() => {
    sessionIdRef.current = sessionId;
    if (skipMessageLoadForSession.current === sessionId) {
      skipMessageLoadForSession.current = "";
      setMessagesLoading(false);
      return;
    }
    if (sessionId) {
      void loadMessages(sessionId);
    } else {
      messageLoadGeneration.current += 1;
      setMessagesLoading(false);
      setMessages([]);
    }
  }, [sessionId, loadMessages]);

  const createSession = async () => {
    if (!selectedAgentId) return;
    const agentId = selectedAgentId;
    const generation = ++sessionLoadGeneration.current;
    setSessionsLoading(true);
    setWorkspaceError("");
    try {
      const id = await client.createSession(agentId);
      if (generation !== sessionLoadGeneration.current || selectedAgentIdRef.current !== agentId) return;
      setSessions((current) => [{ id, agentId }, ...current.filter((session) => session.id !== id)]);
      setSessionId(id);
      setMessages([]);
    } catch (error) {
      if (generation === sessionLoadGeneration.current && selectedAgentIdRef.current === agentId) {
        setWorkspaceError(`${t("session.createFailed")}: ${errorMessage(error)}`);
      }
    } finally {
      if (generation === sessionLoadGeneration.current && selectedAgentIdRef.current === agentId) setSessionsLoading(false);
    }
  };

  const loadExistingSession = async () => {
    const nextSessionId = existingSessionId.trim();
    if (!nextSessionId) {
      setExistingSessionError(t("session.idRequired"));
      return;
    }
    if (!selectedAgentId) return;

    const agentId = selectedAgentId;
    const generation = ++messageLoadGeneration.current;
    setExistingSessionLoading(true);
    setExistingSessionError("");
    setWorkspaceError("");
    try {
      const nextMessages = await client.getMessages(agentId, nextSessionId);
      if (generation !== messageLoadGeneration.current || selectedAgentIdRef.current !== agentId) return;
      setSessions((current) => [
        { id: nextSessionId, agentId, messageCount: nextMessages.length },
        ...current.filter((session) => session.id !== nextSessionId),
      ]);
      if (sessionIdRef.current !== nextSessionId) {
        skipMessageLoadForSession.current = nextSessionId;
        sessionIdRef.current = nextSessionId;
        setSessionId(nextSessionId);
      }
      setMessages(nextMessages);
      setExistingSessionId("");
      setDrawer(null);
    } catch (error) {
      if (generation === messageLoadGeneration.current && selectedAgentIdRef.current === agentId) {
        setExistingSessionError(`${t("session.loadFailed")}: ${errorMessage(error)}`);
      }
    } finally {
      if (generation === messageLoadGeneration.current && selectedAgentIdRef.current === agentId) setExistingSessionLoading(false);
    }
  };

  const updateStreamingMessage = (assistantId: string, reasoningId: string, event: StreamEvent, generation: number, agentId: string) => {
    if (generation !== streamGeneration.current || selectedAgentIdRef.current !== agentId) return;
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
          const index = current.findIndex((message) => message.id === assistantId);
          const copy = [...current];
          copy.splice(Math.max(0, index), 0, { id: reasoningId, role: "reasoning", content: [{ type: "text", text: event.text }] });
          return copy;
        }
        return current.map((message) => message.id === reasoningId
          ? { ...message, content: [{ type: "text", text: `${message.content[0]?.text || ""}${event.text}` }] }
          : message);
      }
      if (event.type === "message.delta") {
        return current.map((message) => message.id === assistantId
          ? { ...message, pending: true, content: [{ type: "text", text: `${message.content[0]?.text || ""}${event.text}` }] }
          : message);
      }
      if (event.type === "error") {
        return current.map((message) => message.id === assistantId
          ? { ...message, pending: false, error: true, content: [{ type: "text", text: event.message }] }
          : message);
      }
      if (event.type === "done") return current.map((message) => message.id === assistantId ? { ...message, pending: false } : message);
      return current;
    });
  };

  const sendMessage = async (text: string) => {
    if (!selectedAgentId || !sessionId) return;
    const agentId = selectedAgentId;
    const activeSessionId = sessionId;
    const generation = ++streamGeneration.current;
    const stamp = `${Date.now()}`;
    const assistantId = `assistant-${stamp}`;
    const reasoningId = `reasoning-${stamp}`;
    setMessages((current) => [
      ...current,
      { id: `user-${stamp}`, role: "user", content: [{ type: "text", text }] },
      { id: assistantId, role: "assistant", content: [{ type: "text", text: "" }], pending: true },
    ]);
    setSending(true);
    setWorkspaceError("");
    try {
      await client.streamMessage(agentId, activeSessionId, text, (event) => updateStreamingMessage(assistantId, reasoningId, event, generation, agentId));
    } catch (error) {
      if (generation !== streamGeneration.current || selectedAgentIdRef.current !== agentId) return;
      setMessages((current) => current.map((message) => message.id === assistantId
        ? { ...message, pending: false, error: true, content: [{ type: "text", text: errorMessage(error) }] }
        : message));
      throw error;
    } finally {
      if (generation === streamGeneration.current && selectedAgentIdRef.current === agentId) setSending(false);
    }
  };

  const performPair = async (replace: boolean) => {
    setPairing(true); setPairError(""); setPairSuccess(false);
    try {
      const next = await localApi.pair(serverUrl.trim(), pairingCode.trim(), replace);
      setStatus((current) => ({ ...current, remote: next.remote }));
      setPairingCode(""); setPairSuccess(true); setConfirmReplace(false);
      await refreshStatus();
    } catch (error) {
      const apiError = error as ApiError;
      if (apiError.code === "PAIRING_REPLACE_CONFIRMATION_REQUIRED") setConfirmReplace(true);
      else setPairError(errorMessage(error));
    } finally { setPairing(false); }
  };

  const submitPair = () => {
    setPairError("");
    if (!/^https?:\/\//i.test(serverUrl.trim())) { setPairError(t("local.invalidServerAddress")); return; }
    if (!pairingCode.trim()) { setPairError(t("local.pairingCodeHint")); return; }
    if (status.remote.paired && status.remote.serverUrl && status.remote.serverUrl !== serverUrl.trim()) setConfirmReplace(true);
    else void performPair(false);
  };

  const unpair = async () => {
    setUnpairing(true);
    try {
      await localApi.unpair();
      setStatus((current) => ({ ...current, remote: { paired: false, connected: false, serverUrl: "" } }));
      setServerUrl(""); setConfirmUnpair(false);
    } catch (error) { setPairError(errorMessage(error)); }
    finally { setUnpairing(false); }
  };

  const openLogs = async () => {
    setDrawer("logs"); setMobileMenu(false);
    try {
      const serverLogs = await localApi.getLogs();
      if (serverLogs.length) setLogs(serverLogs);
    } catch (error) { setLogs((current) => [...current, { timestamp: new Date().toISOString(), level: "error", message: errorMessage(error) }]); }
  };

  const openSettings = async () => {
    setDrawer("settings"); setMobileMenu(false); setSettingsLoading(true); setSettingsError("");
    try { setLocalSettings(await localApi.getSettings()); }
    catch (error) { setSettingsError(errorMessage(error)); }
    finally { setSettingsLoading(false); }
  };

  const saveSettings = async () => {
    setSettingsLoading(true); setSettingsError("");
    try { setLocalSettings(await localApi.updateSettings(localSettings)); }
    catch (error) { setSettingsError(errorMessage(error)); }
    finally { setSettingsLoading(false); }
  };

  if (initializing) return <div className="page-loading"><Spinner /></div>;

  const serviceAvailable = wsConnected && status.healthy !== false;
  const insecure = status.remote.paired && status.remote.serverUrl.startsWith("http://");
  const navigationLocked = sending;
  const visibleWorkspaceError = workspaceError || statusError;

  return (
    <div className="app-shell">
      {mobileMenu ? <button className="sidebar-backdrop" aria-label={t("common.close")} onClick={closeMobileMenu} tabIndex={-1} /> : null}
      <aside id="app-navigation" ref={mobileSidebarRef} className={`sidebar ${mobileMenu ? "is-open" : ""}`} tabIndex={-1}>
        <div className="sidebar__brand">
          <div className="brand-mark"><Network size={17} aria-hidden="true" /></div>
          <div className="sidebar__brand-copy"><strong>Agent-Bridge</strong><span>Local Console</span></div>
        </div>
        <section className="sidebar__section">
          <div className="sidebar__section-title"><span>{t("agent.title")}</span><span>{agents.length}</span></div>
          <div className="sidebar__list">
            {agents.length ? agents.map((agent) => (
              <button
                className={`sidebar__item ${selectedAgentId === agent.id ? "is-active" : ""}`}
                key={agent.id}
                disabled={navigationLocked}
                onClick={() => {
                  if (agent.id !== selectedAgentId) {
                    sessionLoadGeneration.current += 1;
                    messageLoadGeneration.current += 1;
                    setSessions([]); setSessionId(""); setMessages([]);
                  }
                  setSelectedAgentId(agent.id); closeMobileMenu();
                }}
              >
                <span className="sidebar__item-icon"><Bot size={16} aria-hidden="true" /></span>
                <span className="sidebar__item-copy"><strong>{agent.displayName}</strong><span><StatusDot status={connectionStatus(agent.status)} /><span className="sidebar__item-status-text">{t(`agent.${agent.status}`)}</span></span></span>
              </button>
            )) : <div className="sidebar__empty">{t("agent.emptyBody")}</div>}
          </div>
        </section>
        <div className="sidebar__connection">
          <StatusDot status={status.remote.connected ? "online" : status.remote.paired ? "busy" : "offline"} />
          <div><strong>{status.remote.connected ? t("common.connected") : status.remote.state === "connecting" ? t("local.connecting") : status.remote.paired ? t("local.paired") : t("local.unpaired")}</strong><span>{status.remote.serverUrl || t("local.unpairedBody")}</span></div>
        </div>
        <nav className="sidebar__footer sidebar__footer--local">
          <IconButton icon={Cable} label={t("local.remote")} active={drawer === "remote"} onClick={() => { setDrawer("remote"); setMobileMenu(false); }} />
          <IconButton icon={ScrollText} label={t("local.logs")} active={drawer === "logs"} onClick={() => void openLogs()} />
          <IconButton icon={Settings} label={t("common.settings")} active={drawer === "settings"} onClick={() => void openSettings()} />
        </nav>
      </aside>

      <div className="shell-column" inert={mobileMenu ? true : undefined}>
        {insecure ? <div className="top-warning">{t("local.insecure")}</div> : null}
        {visibleWorkspaceError ? <div className="top-warning top-warning--error">{visibleWorkspaceError}</div> : null}
        <Conversation
          agent={selectedAgent}
          sessions={sessions}
          sessionId={sessionId}
          onSelectSession={setSessionId}
          onCreateSession={createSession}
          onRefreshSessions={() => loadSessions(selectedAgentId, sessionId)}
          onLoadSession={() => {
            setExistingSessionId("");
            setExistingSessionError("");
            setDrawer("session");
          }}
          sessionsLoading={sessionsLoading}
          messages={messages}
          messagesLoading={messagesLoading}
          sending={sending}
          enabled={serviceAvailable}
          unavailableTitle={t("local.serviceOffline")}
          unavailableBody={t("local.serviceOfflineBody")}
          onSend={sendMessage}
          onOpenMobileMenu={() => setMobileMenu(true)}
          mobileMenuOpen={mobileMenu}
        />
      </div>

      <Drawer
        open={drawer === "session"}
        title={t("session.loadExisting")}
        description={selectedAgent?.displayName}
        onClose={() => { if (!existingSessionLoading) setDrawer(null); }}
      >
        <div className="form-stack">
          {existingSessionError ? <Notice tone="error">{existingSessionError}</Notice> : null}
          <label className="field">
            <span className="field__label">{t("session.id")}</span>
            <input
              className="mono"
              value={existingSessionId}
              onChange={(event) => setExistingSessionId(event.target.value)}
              onKeyDown={(event) => { if (event.key === "Enter") void loadExistingSession(); }}
              placeholder={t("session.idHint")}
              autoComplete="off"
              disabled={existingSessionLoading}
            />
            <span className="field__hint">{t("session.loadHint")}</span>
          </label>
          <Button variant="primary" onClick={() => void loadExistingSession()} loading={existingSessionLoading}>{t("session.load")}</Button>
        </div>
      </Drawer>

      <Drawer open={drawer === "remote"} title={t("local.remote")} description={status.remote.paired ? status.remote.serverUrl : t("local.unpairedBody")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          {status.remote.paired ? (
            <div className="data-list">
              <div className="data-row"><div className="data-row__copy"><strong>{t("common.status")}</strong><span>{status.remote.connected ? t("common.connected") : status.remote.state === "connecting" ? t("local.connecting") : t("common.disconnected")}</span></div><StatusDot status={status.remote.connected ? "online" : status.remote.state === "connecting" ? "busy" : "offline"} /></div>
              <div className="data-row"><div className="data-row__copy"><strong>{t("local.remoteServer")}</strong><span>{status.remote.serverUrl}</span></div></div>
              {status.remote.deviceId ? <div className="data-row"><div className="data-row__copy"><strong>{t("local.deviceId")}</strong><span className="mono">{truncateMiddle(status.remote.deviceId, 34)}</span></div></div> : null}
              {status.remote.lastError ? <Notice tone="error">{status.remote.lastError}</Notice> : null}
            </div>
          ) : <Notice>{t("local.unpairedBody")}</Notice>}
          {(serverUrl || status.remote.serverUrl).startsWith("http://") ? <Notice tone="warning">{t("local.insecure")}</Notice> : null}
          {pairSuccess ? <Notice tone="success">{t("local.pairSuccess")}</Notice> : null}
          {pairError ? <Notice tone="error">{pairError}</Notice> : null}
          <label className="field"><span className="field__label">{t("local.serverAddress")}</span><input value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} placeholder={t("local.serverAddressHint")} autoComplete="url" /></label>
          <label className="field"><span className="field__label">{t("local.pairingCode")}</span><input className="mono" value={pairingCode} onChange={(event) => setPairingCode(event.target.value.toUpperCase())} placeholder={t("local.pairingCodeHint")} autoComplete="one-time-code" /></label>
          <Button variant="primary" icon={Cable} onClick={submitPair} loading={pairing}>{pairing ? t("local.pairing") : t("local.pair")}</Button>
          {status.remote.paired ? <Button variant="ghost" icon={Unplug} onClick={() => setConfirmUnpair(true)}>{t("local.unpair")}</Button> : null}
        </div>
      </Drawer>

      <Drawer open={drawer === "logs"} title={t("local.logs")} onClose={() => setDrawer(null)} wide>
        {logs.length ? <div className="log-list">{logs.map((entry, index) => (
          <div className={`log-entry log-entry--${entry.level}`} key={`${entry.timestamp}-${index}`}>
            <time>{formatDate(entry.timestamp, locale)}</time><strong>{entry.level}</strong><span>{entry.message}</span>
          </div>
        ))}</div> : <EmptyState compact icon={ScrollText} title={t("local.logs")} body={t("local.logsEmpty")} />}
      </Drawer>

      <Drawer open={drawer === "settings"} title={t("local.settings")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          {settingsError ? <Notice tone="error">{settingsError}</Notice> : null}
          {localSettings.restartRequired ? <Notice tone="success">{t("local.restartRequired")}</Notice> : null}
          <LanguageControl />
          <label className="toggle-row">
            <span className="toggle-row__copy"><strong>{t("local.debug")}</strong><span>{t("local.debugBody")}</span></span>
            <span className="toggle"><input type="checkbox" checked={localSettings.debug} onChange={(event) => setLocalSettings((current) => ({ ...current, debug: event.target.checked, restartRequired: false }))} /><span aria-hidden="true" /></span>
          </label>
          <label className="field"><span className="field__label">{t("local.claudeSettings")}</span><input value={localSettings.claudeSettingsFile} onChange={(event) => setLocalSettings((current) => ({ ...current, claudeSettingsFile: event.target.value, restartRequired: false }))} placeholder={t("local.claudeSettingsHint")} autoComplete="off" /></label>
          <Button variant="primary" onClick={() => void saveSettings()} loading={settingsLoading}>{t("common.save")}</Button>
          <div className="settings-list data-list">
            <div className="data-row"><div className="data-row__copy"><strong>{t("common.version")}</strong><span>{status.version}</span></div></div>
            <div className="data-row"><div className="data-row__copy"><strong>{t("local.listenAddress")}</strong><span className="mono">{status.localAddress}</span></div></div>
            <div className="data-row"><div className="data-row__copy"><strong>{t("local.service")}</strong><span>{serviceAvailable ? t("common.online") : t("common.offline")}</span></div><StatusDot status={serviceAvailable ? "online" : "error"} /></div>
          </div>
          {!serviceAvailable ? <Notice tone="error"><CircleAlert size={14} aria-hidden="true" /> {t("local.serviceOfflineBody")}</Notice> : null}
          <Button variant="secondary" icon={RefreshCw} onClick={() => void refreshStatus()}>{t("common.refresh")}</Button>
        </div>
      </Drawer>

      <ConfirmDialog
        open={confirmReplace}
        title={t("local.replaceTitle")}
        body={t("local.replaceBody")}
        loading={pairing}
        onCancel={() => setConfirmReplace(false)}
        onConfirm={() => void performPair(true)}
      />
      <ConfirmDialog
        open={confirmUnpair}
        title={t("local.unpairTitle")}
        body={t("local.unpairBody")}
        confirmLabel={t("local.unpair")}
        danger
        loading={unpairing}
        onCancel={() => setConfirmUnpair(false)}
        onConfirm={() => void unpair()}
      />
    </div>
  );
}
