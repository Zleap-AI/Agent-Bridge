/**
 * 授权决策对话框组件
 *
 * 展示 Agent 的权限请求内容（Markdown 格式），
 * 提供允许/拒绝/始终允许等操作按钮。
 *
 * @file AuthorizationDialog.tsx
 * @author Lzm
 * @date 2026-07-22
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import { AlertTriangle, LoaderCircle } from "lucide-react";
import { renderMD, ToolCallDetails } from "./renderMD";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface AuthorizationDialogProps {
  open: boolean;
  onClose: () => void;
  onAllow: () => void;
  onDeny: () => void;
  onAllowAlways?: () => void;
  title?: string;
  message: string;
  toolCall?: unknown;
  cwd?: string;
  loading?: boolean;
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function AuthorizationDialog({
  open,
  onClose,
  onAllow,
  onDeny,
  onAllowAlways,
  title,
  message,
  toolCall,
  cwd,
  loading,
}: AuthorizationDialogProps) {
  if (!open) return null;

  return (
    <div className="overlay overlay--center" onClick={onClose}>
      <div className="permission-dialog" onClick={(e) => e.stopPropagation()}>
        <div className="permission-dialog__header">
          <div>
            <h2>
              <AlertTriangle size={16} />
              {title || "权限请求"}
            </h2>
            {cwd && (
              <p>
                会话工作目录: <code>{cwd}</code>
              </p>
            )}
          </div>
        </div>

        <div className="permission-dialog__body">
          {loading ? (
            <div className="spinner-row spinner-row--padded">
              <LoaderCircle className="spin" size={16} />
              等待您的授权决策...
            </div>
          ) : (
            <>
              <div className="md-content">{renderMD(message)}</div>
              <ToolCallDetails toolCall={toolCall} />
            </>
          )}
        </div>

        <div className="permission-dialog__footer">
          <button className="button button--primary" onClick={onAllow} disabled={loading}>
            允许
          </button>
          <button className="button button--secondary" onClick={onDeny} disabled={loading}>
            拒绝
          </button>
          {onAllowAlways && (
            <button className="button button--secondary" onClick={onAllowAlways} disabled={loading}>
              始终允许
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
