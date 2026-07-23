/**
 * 新建会话对话框组件
 *
 * 引导用户完成"选 Agent → 填工作目录 → 选授权模式 → 确认创建"的完整流程。
 *
 * @file NewSessionDialog.tsx
 * @author Lzm
 * @date 2026-07-22
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import { ChevronDown } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useI18n } from "../i18n";
import { Button } from "./ui";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface NewSessionDialogProps {
  open: boolean;
  onClose: () => void;
  onCreate: (agentId: string, cwd: string, permissionMode: string) => void;
  agents: Array<{ id: string; displayName: string }>;
  defaultAgentId?: string;
  defaultCWD?: string;
  loading?: boolean;
  error?: string;
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function NewSessionDialog({
  open,
  onClose,
  onCreate,
  agents,
  defaultAgentId,
  defaultCWD = "",
  loading = false,
  error,
}: NewSessionDialogProps) {
  const { t } = useI18n();
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [cwd, setCwd] = useState(defaultCWD);
  const [permissionMode, setPermissionMode] = useState("request_approval");

  // 当 agents 或 defaultAgentId 变化时更新默认选中
  useEffect(() => {
    if (defaultAgentId && agents.some((a) => a.id === defaultAgentId)) {
      setSelectedAgentId(defaultAgentId);
    } else if (agents.length > 0 && !agents.find((a) => a.id === selectedAgentId)) {
      setSelectedAgentId(agents[0].id);
    }
  }, [agents, defaultAgentId, selectedAgentId]);

  // 重置状态（当对话框关闭时）
  useEffect(() => {
    if (!open) {
      setCwd(defaultCWD);
      setPermissionMode("request_approval");
    }
  }, [open, defaultCWD]);

  const handleCreate = useCallback(() => {
    if (!selectedAgentId) return;
    onCreate(selectedAgentId, cwd, permissionMode);
  }, [selectedAgentId, cwd, permissionMode, onCreate]);

  if (!open) return null;

  return (
    <div className="overlay overlay--center" onClick={onClose}>
      <div className="new-session-dialog" onClick={(e) => e.stopPropagation()}>
        <div className="new-session-dialog__header">
          <h2>{t("session.new")}</h2>
          <p>{t("session.workDirHint")}</p>
        </div>

        <div className="new-session-dialog__body">
          {agents.length > 1 && (
            <label className="field">
              <span className="field__label">Agent</span>
              <div className="session-control session-control--full">
                <select
                  value={selectedAgentId}
                  onChange={(e) => setSelectedAgentId(e.target.value)}
                  disabled={loading}
                >
                  {agents.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.displayName}
                    </option>
                  ))}
                </select>
                <ChevronDown size={15} aria-hidden="true" />
              </div>
            </label>
          )}

          <label className="field">
            <span className="field__label">{t("session.workDir")}</span>
            <input
              className="field__input"
              type="text"
              value={cwd}
              onChange={(e) => setCwd(e.target.value)}
              placeholder={t("session.workDirPlaceholder")}
              disabled={loading}
              spellCheck={false}
              autoComplete="off"
            />
          </label>

          <label className="field">
            <span className="field__label">{t("permission.mode")}</span>
            <div className="permission-control permission-control--full">
              <select
                value={permissionMode}
                onChange={(e) => setPermissionMode(e.target.value)}
                disabled={loading}
              >
                <option value="request_approval">
                  {t("permission.requestApproval")} — {t("permission.requestApprovalDesc")}
                </option>
                <option value="auto_approve">
                  {t("permission.autoApprove")} — {t("permission.autoApproveDesc")}
                </option>
                <option value="session_approval">
                  {t("permission.sessionApproval")} — {t("permission.sessionApprovalDesc")}
                </option>
                <option value="full_access">
                  {t("permission.fullAccess")} — {t("permission.fullAccessDesc")}
                </option>
              </select>
            </div>
          </label>

          {error && (
            <div className="notice notice--error">{error}</div>
          )}
        </div>

        <div className="new-session-dialog__footer">
          <Button onClick={onClose} disabled={loading}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={handleCreate}
            loading={loading}
            disabled={!selectedAgentId}
          >
            {t("session.new")}
          </Button>
        </div>
      </div>
    </div>
  );
}
