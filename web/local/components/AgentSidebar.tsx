/**
 * Agent 侧边栏组件
 *
 * 展示 Agent 列表、连接状态和底部导航按钮。
 *
 * @file AgentSidebar.tsx
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import {
  Bot,
  Cable,
  LoaderCircle,
  Network,
  Play,
  ScrollText,
  Settings,
  Square,
  Stethoscope,
} from "lucide-react";
import type { AgentInfo } from "../../shared/types";
import { useI18n } from "../../shared/i18n";
import { IconButton, StatusDot, useMobileSidebar } from "../../shared/components/ui";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface AgentSidebarProps {
  agents: AgentInfo[];
  activeAgentId: string | null;
  onSelectAgent: (id: string) => void;
  onStartAgent: (id: string) => Promise<void>;
  onStopAgent: (id: string) => Promise<void>;
  pendingActions: Set<string>;
  navigationLocked: boolean;
  // 连接状态
  remoteConnected: boolean;
  remotePaired: boolean;
  remoteState?: string;
  remoteServerUrl?: string;
  // 底部按钮
  onOpenRemote: () => void;
  onOpenDiagnostics: () => void;
  onOpenLogs: () => void;
  onOpenSettings: () => void;
  // 移动端
  mobileMenu: boolean;
  onCloseMobile: () => void;
}

/* ─── 辅助函数 ─────────────────────────────────────────────── */

function connectionDot(status: AgentInfo["status"]): "online" | "busy" | "offline" | "error" {
  if (status === "idle") return "online";
  if (status === "busy") return "busy";
  if (status === "error") return "error";
  return "offline";
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function AgentSidebar({
  agents,
  activeAgentId,
  onSelectAgent,
  onStartAgent,
  onStopAgent,
  pendingActions,
  navigationLocked,
  remoteConnected,
  remotePaired,
  remoteState,
  remoteServerUrl,
  onOpenRemote,
  onOpenDiagnostics,
  onOpenLogs,
  onOpenSettings,
  mobileMenu,
  onCloseMobile,
}: AgentSidebarProps) {
  const { t } = useI18n();
  const sidebarRef = useMobileSidebar(mobileMenu, onCloseMobile);

  return (
    <>
      {mobileMenu ? (
        <button
          className="sidebar-backdrop"
          aria-label={t("common.close")}
          onClick={onCloseMobile}
          tabIndex={-1}
        />
      ) : null}
      <aside
        id="app-navigation"
        ref={sidebarRef}
        className={`sidebar ${mobileMenu ? "is-open" : ""}`}
        tabIndex={-1}
      >
        {/* 品牌区 */}
        <div className="sidebar__brand">
          <div className="brand-mark">
            <Network size={17} aria-hidden="true" />
          </div>
          <div className="sidebar__brand-copy">
            <strong>Agent-Bridge</strong>
            <span>Local Console</span>
          </div>
        </div>

        {/* Agent 列表 */}
        <section className="sidebar__section">
          <div className="sidebar__section-title">
            <span>{t("agent.title")}</span>
            <span>{agents.length}</span>
          </div>
          <div className="sidebar__list">
            {agents.length ? (
              agents.map((agent) => (
                <div className="sidebar__agent-row" key={agent.id}>
                  <button
                    className={`sidebar__item ${activeAgentId === agent.id ? "is-active" : ""}`}
                    disabled={navigationLocked}
                    onClick={() => {
                      onSelectAgent(agent.id);
                      onCloseMobile();
                    }}
                  >
                    <span className="sidebar__item-icon">
                      <Bot size={16} aria-hidden="true" />
                    </span>
                    <span className="sidebar__item-copy">
                      <strong>{agent.displayName}</strong>
                      <span>
                        <StatusDot status={connectionDot(agent.status)} />
                        <span className="sidebar__item-status-text">
                          {t(`agent.${agent.status}`)}
                        </span>
                      </span>
                    </span>
                  </button>
                  <button
                    className="sidebar__item-action"
                    onClick={(event) => {
                      event.stopPropagation();
                      void (agent.status === "disconnected"
                        ? onStartAgent(agent.id)
                        : onStopAgent(agent.id));
                    }}
                    disabled={navigationLocked || pendingActions.has(agent.id)}
                    title={
                      agent.status === "disconnected"
                        ? t("agent.start")
                        : t("agent.stop")
                    }
                    aria-label={
                      agent.status === "disconnected"
                        ? t("agent.start")
                        : t("agent.stop")
                    }
                  >
                    {pendingActions.has(agent.id) ? (
                      <LoaderCircle size={14} className="spin" aria-hidden="true" />
                    ) : agent.status === "disconnected" ? (
                      <Play size={14} aria-hidden="true" />
                    ) : (
                      <Square size={14} aria-hidden="true" />
                    )}
                  </button>
                </div>
              ))
            ) : (
              <div className="sidebar__empty">{t("agent.emptyBody")}</div>
            )}
          </div>
        </section>

        {/* 连接状态 */}
        <div className="sidebar__connection">
          <StatusDot
            status={
              remoteConnected
                ? "online"
                : remotePaired
                  ? "busy"
                  : "offline"
            }
          />
          <div>
            <strong>
              {remoteConnected
                ? t("common.connected")
                : remoteState === "connecting"
                  ? t("local.connecting")
                  : remotePaired
                    ? t("local.paired")
                    : t("local.unpaired")}
            </strong>
            <span>{remoteServerUrl || t("local.unpairedBody")}</span>
          </div>
        </div>

        {/* 底部导航 */}
        <nav className="sidebar__footer sidebar__footer--local">
          <IconButton
            icon={Cable}
            label={t("local.remote")}
            onClick={onOpenRemote}
          />
          <IconButton
            icon={Stethoscope}
            label={t("local.diagnostics")}
            onClick={onOpenDiagnostics}
          />
          <IconButton
            icon={ScrollText}
            label={t("local.logs")}
            onClick={onOpenLogs}
          />
          <IconButton
            icon={Settings}
            label={t("common.settings")}
            onClick={onOpenSettings}
          />
        </nav>
      </aside>
    </>
  );
}
