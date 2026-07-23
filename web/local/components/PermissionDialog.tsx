/**
 * 权限请求弹窗组件
 *
 * 展示 Agent 的权限请求详情、授权历史记录，并提供允许/拒绝/始终允许操作。
 *
 * @file PermissionDialog.tsx
 * @author Lzm
 * @date 2026-07-22
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import { AlertTriangle, LoaderCircle } from "lucide-react";
import { useI18n } from "../../shared/i18n";
import { renderMD, ToolCallDetails } from "../../shared/components/renderMD";
import type { PermissionRequestEvent } from "../../shared/types";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface PermissionDialogProps {
  open: boolean;
  request: PermissionRequestEvent | null;
  history: PermissionRequestEvent[];
  loading: boolean;
  onAllow: () => void;
  onDeny: () => void;
  onAllowAlways: () => void;
  onClose: () => void;
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function PermissionDialog({
  open,
  request,
  history,
  loading,
  onAllow,
  onDeny,
  onAllowAlways,
  onClose,
}: PermissionDialogProps) {
  const { t } = useI18n();

  // 调试：记录弹窗渲染状态
  console.debug(`[DIALOG_DEBUG] PermissionDialog 渲染: open=${open}, request=${request ? "yes" : "no"}, loading=${loading}`,
    request ? `session_id=${request.session_id?.slice(0, 16)}, agent_id=${request.agent_id}` : "");

  if (!open || !request) return null;

  return (
    <div className="overlay overlay--center" onClick={onClose}>
      <div className="permission-dialog" onClick={(event) => event.stopPropagation()}>
        {/* 弹窗头部 */}
        <div className="permission-dialog__header">
          <div>
            <h2>
              <AlertTriangle size={16} />
              {t("permission.title")}
            </h2>
            {request.session_cwd && (
              <p>
                {t("session.workDir")}: <code>{request.session_cwd}</code>
              </p>
            )}
          </div>
        </div>

        {/* 弹窗主体 */}
        <div className="permission-dialog__body">
          {loading ? (
            <div className="spinner-row spinner-row--padded">
              <LoaderCircle className="spin" size={16} />
              {t("permission.waiting")}
            </div>
          ) : (
            <>
              {request.message ? (
                <div className="md-content">{renderMD(request.message)}</div>
              ) : null}

              <ToolCallDetails toolCall={request.tool_call} label={t("permission.toolCall")} />

              {/* 授权历史 */}
              {history.length > 0 && (
                <div className="permission-history">
                  <h3 className="permission-history__heading">
                    {t("permission.history")}
                  </h3>
                  <div className="permission-history__list">
                    {[...history].reverse().slice(0, 20).map((entry, index) => {
                      const isDenied = entry.params &&
                        typeof entry.params === "object" &&
                        (entry.params as Record<string, unknown>).allowed === false;
                      const isAuto = entry.params &&
                        typeof entry.params === "object" &&
                        (entry.params as Record<string, unknown>).permission_mode === "auto_approve";
                      return (
                        <div
                          className={`permission-history__item ${isAuto ? "permission-history__item--auto" : isDenied ? "permission-history__item--denied" : "permission-history__item--approved"}`}
                          key={index}
                        >
                          <span className={`status-dot status-dot--${isDenied ? "error" : "online"}`} aria-hidden="true" />
                          <strong>{isAuto ? t("permission.allowAlways") : isDenied ? t("permission.denied") : t("permission.approved")}</strong>
                          <span className="mono">{entry.session_id?.slice(0, 12)}</span>
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}
            </>
          )}
        </div>

        {/* 弹窗底部操作按钮 */}
        <div className="permission-dialog__footer">
          <button
            className="button button--primary"
            onClick={() => { console.debug("[DIALOG_DEBUG] 允许按钮被点击, loading=", loading); onAllow(); }}
            disabled={loading}
          >
            {t("permission.allow")}
          </button>
          <button
            className="button button--secondary"
            onClick={() => { console.debug("[DIALOG_DEBUG] 拒绝按钮被点击, loading=", loading); onDeny(); }}
            disabled={loading}
          >
            {t("permission.deny")}
          </button>
          {onAllowAlways && (
            <button
              className="button button--secondary"
              onClick={() => { console.debug("[DIALOG_DEBUG] 始终允许按钮被点击, loading=", loading); onAllowAlways(); }}
              disabled={loading}
            >
              {t("permission.allowAlways")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
