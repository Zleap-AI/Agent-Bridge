/**
 * Agent-Bridge Local Console 入口组件
 *
 * 使用子组件和 hooks 整合 Agent 管理、会话管理、消息管理、权限管理和 Toast 通知。
 *
 * @file App.tsx
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import {
  Cable,
  Check,
  CircleAlert,
  LoaderCircle,
  Minus,
  RefreshCw,
  ScrollText,
  Unplug,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalAdminClient, localApi, type LocalSettings } from "../shared/api/local";
import { Conversation } from "../shared/components/Conversation";
import {
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  LanguageControl,
  NewSessionDialog,
  Notice,
  Spinner,
} from "../shared/components/ui";
import { formatDate, truncateMiddle } from "../shared/format";
import { useI18n } from "../shared/i18n";
import type {
  DiagnosticsInfo,
  LocalLogEntry,
  LocalStatus,
  StorageInfo,
} from "../shared/types";
import { useToastContext } from "./hooks/useToast";
import { usePermission } from "./hooks/usePermission";
import { useMessages } from "./hooks/useMessages";
import { useSessions } from "./hooks/useSessions";
import { useAgents } from "./hooks/useAgents";
import { AgentSidebar } from "./components/AgentSidebar";
import { PermissionDialog } from "./components/PermissionDialog";

/* ─── 常量 ─────────────────────────────────────────────────── */

const initialStatus: LocalStatus = {
  version: "0.5.0",
  localAddress: "127.0.0.1:9202",
  healthy: false,
  agents: [],
  remote: { paired: false, connected: false, serverUrl: "" },
};

type DrawerName = "remote" | "logs" | "settings" | "diagnostics" | "session" | null;

/* ─── 辅助 ─────────────────────────────────────────────────── */

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function LocalApp() {
  const { t, locale } = useI18n();
  const client = useMemo(() => new LocalAdminClient(), []);

  // Hooks
  const {
    agents,
    activeAgentId,
    setActiveAgent,
    activeAgent,
    pendingActions,
    refresh: refreshAgents,
    startAgent,
    stopAgent,
  } = useAgents(client);

  const sessionsHook = useSessions(client, activeAgentId);
  const { sessions, sessionId, create: createSession, loadExisting: loadSession, loading: sessionsLoading } = sessionsHook;
  const streamedSessionIdRef = useRef("");

  const messagesHook = useMessages(client, {
    onSessionUpdate: (sid, aid) => {
      streamedSessionIdRef.current = sid;
      sessionsHook.applyStreamUpdate(aid, sid);
    },
  });
  const { messages, loading: messagesLoading, sending } = messagesHook;

  const permissionHook = usePermission(client);
  const { toast } = useToastContext();

  // ── 本地状态 ──────────────────────────────────────────────────────────

  const [initializing, setInitializing] = useState(true);
  const [status, setStatus] = useState<LocalStatus>(initialStatus);
  const [wsConnected, setWsConnected] = useState(false);
  const [mobileMenu, setMobileMenu] = useState(false);
  const [drawer, setDrawer] = useState<DrawerName>(null);
  const [logs, setLogs] = useState<LocalLogEntry[]>([]);
  const [serverUrl, setServerUrl] = useState("");
  const [pairingCode, setPairingCode] = useState("");
  const [pairing, setPairing] = useState(false);
  const [pairError, setPairError] = useState("");
  const [pairSuccess, setPairSuccess] = useState(false);
  const [confirmReplace, setConfirmReplace] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);
  const [unpairing, setUnpairing] = useState(false);
  const [settings, setSettings] = useState<LocalSettings>({ debug: false, claudeSettingsFile: "", restartRequired: false });
  const [settingsLoading, setSettingsLoading] = useState(false);
  const [settingsError, setSettingsError] = useState("");
  const [storageInfo, setStorageInfo] = useState<StorageInfo | null>(null);
  const [diagnostics, setDiagnostics] = useState<DiagnosticsInfo | null>(null);
  const [diagnosticsLoading, setDiagnosticsLoading] = useState(false);
  const [diagnosticsError, setDiagnosticsError] = useState("");
  const [existingSessionId, setExistingSessionId] = useState("");
  const [existingSessionLoading, setExistingSessionLoading] = useState(false);
  const [existingSessionError, setExistingSessionError] = useState("");
  const [newSessionDialogOpen, setNewSessionDialogOpen] = useState(false);
  const [insecure, setInsecure] = useState(false);

  // ── 副作用：客户端连接 ────────────────────────────────────────────────

  useEffect(() => {
    const offConnection = client.onConnection((connected) => {
      setWsConnected(connected);
    });
    const offLog = client.onLog((level, message) => {
      setLogs((prev) => [...prev.slice(-199), { timestamp: new Date().toISOString(), level, message }]);
    });
    client.connect();
    void refreshAgents().finally(() => setInitializing(false));
    const timer = window.setInterval(() => void refreshAgents(), 5000);
    return () => {
      window.clearInterval(timer);
      offConnection();
      offLog();
      client.close();
    };
  }, [client, refreshAgents]);

  // ── 副作用：同步 status ───────────────────────────────────────────────

  useEffect(() => {
    if (!wsConnected) return;
    const id = window.setInterval(async () => {
      try {
        const next = await localApi.getStatus();
        setStatus(next);
        setInsecure(next.remote.paired && next.remote.serverUrl.startsWith("http://"));
      } catch { /* 健康检查失败静默 */ }
    }, 10000);
    void localApi.getStatus().then(setStatus).catch(() => {});
    return () => window.clearInterval(id);
  }, [wsConnected]);

  useEffect(() => {
    if (wsConnected && activeAgentId) {
      void sessionsHook.refresh(activeAgentId);
    }
  }, [wsConnected, activeAgentId, sessionsHook.refresh]);

  // ── 副作用：session 变化时加载消息 ────────────────────────────────────

  useEffect(() => {
    if (activeAgentId && sessionId) {
      if (streamedSessionIdRef.current === sessionId) {
        streamedSessionIdRef.current = "";
        return;
      }
      void messagesHook.load(activeAgentId, sessionId);
    }
  }, [activeAgentId, sessionId]);

  // ── 事件处理器 ────────────────────────────────────────────────────────

  const handleSelectAgent = useCallback((id: string) => {
    if (id !== activeAgentId) {
      messagesHook.clear();
      setActiveAgent(id);
    }
  }, [activeAgentId, messagesHook, setActiveAgent]);

  const handleCreateSession = useCallback(async (agentId: string, cwd: string, permissionMode: string) => {
    try {
      await createSession(agentId, cwd, permissionMode);
      toast.success(t("session.created"));
      setNewSessionDialogOpen(false);
    } catch (err) {
      toast.error(`${t("session.createFailed")}: ${errorMessage(err)}`);
    }
  }, [createSession, t, toast]);

  const handleSend = useCallback(async (text: string) => {
    if (!activeAgentId || !sessionId) return;
    await messagesHook.send(activeAgentId, sessionId, text);
  }, [activeAgentId, sessionId, messagesHook]);

  const handlePermissionAllow = useCallback(async () => {
    console.debug("[APP_DEBUG] handlePermissionAllow 被调用");
    try {
      await permissionHook.allow();
      console.debug("[APP_DEBUG] handlePermissionAllow 完成，显示 success toast");
      toast.success(t("permission.approved"));
    } catch (e) {
      console.error("[APP_DEBUG] handlePermissionAllow 异常:", e);
    }
  }, [permissionHook, t, toast]);

  const handlePermissionDeny = useCallback(async () => {
    console.debug("[APP_DEBUG] handlePermissionDeny 被调用");
    try {
      await permissionHook.deny();
      console.debug("[APP_DEBUG] handlePermissionDeny 完成，显示 info toast");
      toast.info(t("permission.denied"));
    } catch (e) {
      console.error("[APP_DEBUG] handlePermissionDeny 异常:", e);
    }
  }, [permissionHook, t, toast]);

  const handlePermissionAllowAlways = useCallback(async () => {
    console.debug("[APP_DEBUG] handlePermissionAllowAlways 被调用");
    try {
      await permissionHook.allowAlways();
      console.debug("[APP_DEBUG] handlePermissionAllowAlways 完成，显示 success toast");
      toast.success(t("permission.allowAlways"));
    } catch (e) {
      console.error("[APP_DEBUG] handlePermissionAllowAlways 异常:", e);
    }
  }, [permissionHook, t, toast]);

  // ── 远程配对 ──────────────────────────────────────────────────────────

  const performPair = useCallback(async (replace: boolean) => {
    setPairing(true);
    setPairError("");
    setPairSuccess(false);
    try {
      const next = await localApi.pair(serverUrl.trim(), pairingCode.trim(), replace);
      setStatus((prev) => ({ ...prev, remote: next.remote }));
      setPairingCode("");
      setPairSuccess(true);
      setConfirmReplace(false);
      void refreshAgents();
    } catch (error) {
      const apiError = error as { code?: string };
      if (apiError.code === "PAIRING_REPLACE_CONFIRMATION_REQUIRED") setConfirmReplace(true);
      else setPairError(errorMessage(error));
    } finally {
      setPairing(false);
    }
  }, [serverUrl, pairingCode, refreshAgents]);

  const submitPair = useCallback(() => {
    setPairError("");
    if (!/^https?:\/\//i.test(serverUrl.trim())) { setPairError(t("local.invalidServerAddress")); return; }
    if (!pairingCode.trim()) { setPairError(t("local.pairingCodeHint")); return; }
    if (status.remote.paired && status.remote.serverUrl && status.remote.serverUrl !== serverUrl.trim()) setConfirmReplace(true);
    else void performPair(false);
  }, [serverUrl, pairingCode, status.remote, performPair, t]);

  const unpair = useCallback(async () => {
    setUnpairing(true);
    try {
      await localApi.unpair();
      setStatus((prev) => ({ ...prev, remote: { paired: false, connected: false, serverUrl: "" } }));
      setServerUrl("");
      setConfirmUnpair(false);
    } catch (error) { setPairError(errorMessage(error)); }
    finally { setUnpairing(false); }
  }, []);

  // ── 抽屉打开 ──────────────────────────────────────────────────────────

  const openDrawer = useCallback((name: DrawerName) => {
    setDrawer(name);
    setMobileMenu(false);
  }, []);

  const openSettings = useCallback(async () => {
    openDrawer("settings");
    setSettingsLoading(true);
    setSettingsError("");
    try {
      const [settingsResult, storageResult] = await Promise.all([
        localApi.getSettings(),
        localApi.getStorageInfo().catch(() => null),
      ]);
      setSettings(settingsResult);
      setStorageInfo(storageResult);
    } catch (error) { setSettingsError(errorMessage(error)); }
    finally { setSettingsLoading(false); }
  }, [openDrawer]);

  const saveSettings = useCallback(async () => {
    setSettingsLoading(true);
    setSettingsError("");
    try { setSettings(await localApi.updateSettings(settings)); }
    catch (error) { setSettingsError(errorMessage(error)); }
    finally { setSettingsLoading(false); }
  }, [settings]);

  const openDiagnostics = useCallback(async () => {
    openDrawer("diagnostics");
    setDiagnosticsLoading(true);
    setDiagnosticsError("");
    try { setDiagnostics(await localApi.getDiagnostics()); }
    catch (error) { setDiagnosticsError(errorMessage(error)); }
    finally { setDiagnosticsLoading(false); }
  }, [openDrawer]);

  const openLogs = useCallback(async () => {
    openDrawer("logs");
    try {
      const serverLogs = await localApi.getLogs();
      if (serverLogs.length) setLogs(serverLogs);
    } catch (error) {
      setLogs((prev) => [...prev, { timestamp: new Date().toISOString(), level: "error", message: errorMessage(error) }]);
    }
  }, [openDrawer]);

  const loadExistingSession = useCallback(async () => {
    const nextSessionId = existingSessionId.trim();
    if (!nextSessionId) { setExistingSessionError(t("session.idRequired")); return; }
    if (!activeAgentId) return;
    setExistingSessionLoading(true);
    setExistingSessionError("");
    try {
      await loadSession(activeAgentId, nextSessionId);
      setExistingSessionId("");
      setDrawer(null);
    } catch (error) { setExistingSessionError(`${t("session.loadFailed")}: ${errorMessage(error)}`); }
    finally { setExistingSessionLoading(false); }
  }, [existingSessionId, activeAgentId, loadSession, t]);

  // ── 初始加载 ──────────────────────────────────────────────────────────

  if (initializing) return <div className="page-loading"><Spinner /></div>;

  const serviceAvailable = wsConnected && status.healthy !== false;
  const navigationLocked = sending;

  return (
    <div className="app-shell">
      <AgentSidebar
        agents={agents}
        activeAgentId={activeAgentId}
        onSelectAgent={handleSelectAgent}
        onStartAgent={startAgent}
        onStopAgent={stopAgent}
        pendingActions={pendingActions}
        navigationLocked={navigationLocked}
        remoteConnected={status.remote.connected}
        remotePaired={status.remote.paired}
        remoteState={status.remote.state}
        remoteServerUrl={status.remote.serverUrl}
        onOpenRemote={() => openDrawer("remote")}
        onOpenDiagnostics={openDiagnostics}
        onOpenLogs={openLogs}
        onOpenSettings={openSettings}
        mobileMenu={mobileMenu}
        onCloseMobile={() => setMobileMenu(false)}
      />

      <div className="shell-column" inert={mobileMenu ? true : undefined}>
        {insecure ? <div className="top-warning">{t("local.insecure")}</div> : null}
        <Conversation
          agent={activeAgent}
          sessions={sessions}
          sessionId={sessionId}
          onSelectSession={(id) => sessionsHook.setSessionId(id)}
          onCreateSession={() => setNewSessionDialogOpen(true)}
          onRefreshSessions={() => activeAgentId ? void sessionsHook.refresh(activeAgentId) : undefined}
          onLoadSession={() => { setExistingSessionId(""); setExistingSessionError(""); openDrawer("session"); }}
          sessionsLoading={sessionsLoading || messagesLoading}
          messages={messages}
          messagesLoading={messagesLoading}
          sending={sending}
          enabled={serviceAvailable}
          unavailableTitle={t("local.serviceOffline")}
          unavailableBody={t("local.serviceOfflineBody")}
          onSend={handleSend}
          onOpenMobileMenu={() => setMobileMenu(true)}
          mobileMenuOpen={mobileMenu}
        />
      </div>

      {/* 抽屉：加载已有 Session */}
      <Drawer
        open={drawer === "session"}
        title={t("session.loadExisting")}
        description={activeAgent?.displayName}
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
          <Button variant="primary" onClick={() => void loadExistingSession()} loading={existingSessionLoading}>
            {t("session.load")}
          </Button>
        </div>
      </Drawer>

      {/* 抽屉：远程连接 */}
      <Drawer
        open={drawer === "remote"}
        title={t("local.remote")}
        description={status.remote.paired ? status.remote.serverUrl : t("local.unpairedBody")}
        onClose={() => setDrawer(null)}
      >
        <div className="form-stack">
          {status.remote.paired ? (
            <div className="data-list">
              <div className="data-row">
                <div className="data-row__copy">
                  <strong>{t("common.status")}</strong>
                  <span>{status.remote.connected ? t("common.connected") : status.remote.state === "connecting" ? t("local.connecting") : t("common.disconnected")}</span>
                </div>
              </div>
              <div className="data-row">
                <div className="data-row__copy"><strong>{t("local.remoteServer")}</strong><span>{status.remote.serverUrl}</span></div>
              </div>
              {status.remote.deviceId ? (
                <div className="data-row">
                  <div className="data-row__copy"><strong>{t("local.deviceId")}</strong><span className="mono">{truncateMiddle(status.remote.deviceId, 34)}</span></div>
                </div>
              ) : null}
              {status.remote.lastError ? <Notice tone="error">{status.remote.lastError}</Notice> : null}
            </div>
          ) : <Notice>{t("local.unpairedBody")}</Notice>}
          {pairSuccess ? <Notice tone="success">{t("local.pairSuccess")}</Notice> : null}
          {pairError ? <Notice tone="error">{pairError}</Notice> : null}
          <label className="field">
            <span className="field__label">{t("local.serverAddress")}</span>
            <input value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} placeholder={t("local.serverAddressHint")} autoComplete="url" />
          </label>
          <label className="field">
            <span className="field__label">{t("local.pairingCode")}</span>
            <input className="mono" value={pairingCode} onChange={(event) => setPairingCode(event.target.value.toUpperCase())} placeholder={t("local.pairingCodeHint")} autoComplete="one-time-code" />
          </label>
          <Button variant="primary" icon={Cable} onClick={submitPair} loading={pairing}>
            {pairing ? t("local.pairing") : t("local.pair")}
          </Button>
          {status.remote.paired ? <Button variant="ghost" icon={Unplug} onClick={() => setConfirmUnpair(true)}>{t("local.unpair")}</Button> : null}
        </div>
      </Drawer>

      {/* 抽屉：日志 */}
      <Drawer open={drawer === "logs"} title={t("local.logs")} onClose={() => setDrawer(null)} wide>
        {logs.length ? (
          <div className="log-list">
            {logs.map((entry, index) => (
              <div className={`log-entry log-entry--${entry.level}`} key={`${entry.timestamp}-${index}`}>
                <time>{formatDate(entry.timestamp, locale)}</time><strong>{entry.level}</strong><span>{entry.message}</span>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState compact icon={ScrollText} title={t("local.logs")} body={t("local.logsEmpty")} />
        )}
      </Drawer>

      {/* 抽屉：设置 */}
      <Drawer open={drawer === "settings"} title={t("local.settings")} onClose={() => setDrawer(null)}>
        <div className="form-stack">
          {settingsError ? <Notice tone="error">{settingsError}</Notice> : null}
          {settings.restartRequired ? <Notice tone="success">{t("local.restartRequired")}</Notice> : null}
          <LanguageControl />
          <label className="toggle-row">
            <span className="toggle-row__copy"><strong>{t("local.debug")}</strong><span>{t("local.debugBody")}</span></span>
            <span className="toggle">
              <input type="checkbox" checked={settings.debug} onChange={(event) => setSettings((prev) => ({ ...prev, debug: event.target.checked, restartRequired: false }))} />
              <span aria-hidden="true" />
            </span>
          </label>
          <label className="field">
            <span className="field__label">{t("local.claudeSettings")}</span>
            <input value={settings.claudeSettingsFile} onChange={(event) => setSettings((prev) => ({ ...prev, claudeSettingsFile: event.target.value, restartRequired: false }))} placeholder={t("local.claudeSettingsHint")} autoComplete="off" />
          </label>
          <Button variant="primary" onClick={() => void saveSettings()} loading={settingsLoading}>{t("common.save")}</Button>
          <div className="settings-list data-list">
            <div className="data-row"><div className="data-row__copy"><strong>{t("common.version")}</strong><span>{status.version}</span></div></div>
            <div className="data-row"><div className="data-row__copy"><strong>{t("local.listenAddress")}</strong><span className="mono">{status.localAddress}</span></div></div>
            <div className="data-row"><div className="data-row__copy"><strong>{t("local.service")}</strong><span>{serviceAvailable ? t("common.online") : t("common.offline")}</span></div></div>
          </div>
          {storageInfo ? (
            <div className="settings-list data-list">
              <div className="data-row"><div className="data-row__copy"><strong>{t("local.storage")}</strong><span>{storageInfo.store_dir}</span></div></div>
              <div className="data-row"><div className="data-row__copy"><strong>{t("local.storageStats")}</strong><span>{t("local.storageStats", { sessions: storageInfo.total_sessions, messages: storageInfo.total_messages })}</span></div></div>
            </div>
          ) : null}
          {!serviceAvailable ? <Notice tone="error"><CircleAlert size={14} aria-hidden="true" /> {t("local.serviceOfflineBody")}</Notice> : null}
          <Button variant="secondary" icon={RefreshCw} onClick={() => void refreshAgents()}>{t("common.refresh")}</Button>
        </div>
      </Drawer>

      {/* 抽屉：诊断 */}
      <Drawer open={drawer === "diagnostics"} title={t("local.diagnostics")} description={t("local.diagnosticsBody")} onClose={() => setDrawer(null)} wide>
        {diagnosticsLoading ? (
          <div className="spinner-row spinner-row--centered"><LoaderCircle size={16} className="spin" /> {t("common.loading")}</div>
        ) : diagnosticsError ? (
          <Notice tone="error">{diagnosticsError}</Notice>
        ) : diagnostics ? (
          <div className="diagnostics-panel">
            <section className="diagnostics-section">
              <h3 className="diagnostics-section__title">{t("local.diagRuntime")}</h3>
              <div className="diagnostics-grid">
                {diagnostics.runtime.map((r) => (
                  <div key={r.command} className={`diagnostics-card ${r.found ? "is-ok" : "is-fail"}`}>
                    <div className="diagnostics-card__header">
                      <span className={`status-badge ${r.found ? "status-badge--ok" : "status-badge--fail"}`}>{r.found ? <Check size={12} /> : <X size={12} />}</span>
                      <strong>{r.name}</strong>
                    </div>
                    <div className="diagnostics-card__body">
                      <span className="diagnostics-label">{t("local.diagVersion")}</span>
                      <span className="diagnostics-value">{r.version || t("local.diagNotInstalled")}</span>
                      {r.path ? <><span className="diagnostics-label">{t("local.diagPathValue")}</span><span className="diagnostics-value mono">{r.path}</span></> : null}
                    </div>
                  </div>
                ))}
              </div>
            </section>
            <section className="diagnostics-section">
              <h3 className="diagnostics-section__title">{t("local.diagPath")}</h3>
              <div className="data-list">
                <div className="data-row"><div className="data-row__copy"><strong>{t("local.diagPathCount", { count: diagnostics.path.count })}</strong><span>{diagnostics.path.has_node_modules ? t("local.diagHasNodeModules") : ""}</span></div></div>
              </div>
            </section>
            <section className="diagnostics-section">
              <h3 className="diagnostics-section__title">{t("local.diagAgents")} ({diagnostics.agents.length})</h3>
              <div className="table-wrap">
                <table className="data-table">
                  <thead><tr><th>{t("local.diagAgent")}</th><th>{t("local.diagInstalled")}</th><th>{t("local.diagACP")}</th><th>{t("local.diagBridgeStatus")}</th><th>{t("local.diagConfigDirs")}</th><th>{t("local.diagEnvKeys")}</th></tr></thead>
                  <tbody>
                    {diagnostics.agents.map((agent) => (
                      <tr key={agent.id}>
                        <td><strong>{agent.display}</strong></td>
                        <td><span className={`status-text ${agent.installed ? "status-text--success" : "status-text--error"}`}>{agent.installed ? <Check size={12} /> : <X size={12} />} {agent.installed ? t("local.diagFound") : t("local.diagNotFound")}</span></td>
                        <td><span className={`status-text ${agent.acp_available ? "status-text--success" : (agent.installed ? "status-text--error" : "")}`}>{agent.acp_available ? <Check size={12} /> : agent.installed ? <X size={12} /> : <Minus size={12} />}</span></td>
                        <td><span className={`status-text status-text--${agent.bridge_status === "idle" ? "success" : agent.bridge_status === "busy" ? "running" : agent.bridge_status === "error" ? "error" : ""}`}>{agent.bridge_status}</span></td>
                        <td>{agent.config_dirs.map((dir, i) => <span key={i} className={`diag-dir-item ${dir.exists ? "diag-dir-item--ok" : "diag-dir-item--missing"}`}>{dir.exists ? <Check size={10} /> : <X size={10} />} {dir.path}</span>)}</td>
                        <td>{agent.env_keys.map((env, i) => <span key={i} className={`diag-env-item ${env.set ? "diag-env-item--ok" : "diag-env-item--missing"}`}>{env.set ? <Check size={10} /> : <X size={10} />} {env.key}</span>)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
            {diagnostics.npm_global_agents.length > 0 ? (
              <section className="diagnostics-section">
                <h3 className="diagnostics-section__title">{t("local.diagNPMGlobal")}</h3>
                <div className="table-wrap">
                  <table className="data-table"><thead><tr><th>{t("common.name")}</th><th>{t("local.diagVersion")}</th></tr></thead>
                    <tbody>{diagnostics.npm_global_agents.map((pkg) => <tr key={pkg.name}><td><strong>{pkg.name}</strong></td><td>{pkg.version}</td></tr>)}</tbody>
                  </table>
                </div>
              </section>
            ) : null}
          </div>
        ) : null}
        <div className="form-actions form-actions--section">
          <Button variant="secondary" icon={RefreshCw} onClick={() => void openDiagnostics()} loading={diagnosticsLoading}>{t("common.refresh")}</Button>
        </div>
      </Drawer>

      {/* 确认对话框 */}
      <ConfirmDialog open={confirmReplace} title={t("local.replaceTitle")} body={t("local.replaceBody")} loading={pairing} onCancel={() => setConfirmReplace(false)} onConfirm={() => void performPair(true)} />
      <ConfirmDialog open={confirmUnpair} title={t("local.unpairTitle")} body={t("local.unpairBody")} confirmLabel={t("local.unpair")} danger loading={unpairing} onCancel={() => setConfirmUnpair(false)} onConfirm={() => void unpair()} />

      {/* 新建会话弹窗 */}
      <NewSessionDialog
        open={newSessionDialogOpen}
        onClose={() => setNewSessionDialogOpen(false)}
        onCreate={handleCreateSession}
        agents={agents.filter((a) => a.status === "idle" || a.status === "busy")}
        defaultAgentId={activeAgentId ?? undefined}
        defaultCWD=""
        loading={sending}
      />

      {/* 权限请求弹窗 */}
      <PermissionDialog
        open={permissionHook.dialogOpen}
        request={permissionHook.request}
        history={permissionHook.history}
        loading={permissionHook.loading}
        onAllow={handlePermissionAllow}
        onDeny={handlePermissionDeny}
        onAllowAlways={handlePermissionAllowAlways}
        onClose={() => permissionHook.close()}
      />
    </div>
  );
}
