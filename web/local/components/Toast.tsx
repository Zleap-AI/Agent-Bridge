/* ============================================================
 * Toast 通知渲染组件
 *
 * 固定定位在页面右上角，垂直排列展示 Toast 通知。
 * 支持淡入动画、按类型着色（success / error / info / warning）。
 *
 * @author Lzm
 * @date 2026-07-21
 * @encoding UTF-8
 * @lang TypeScript 5.x (JSX)
 * ============================================================ */

import {
  AlertTriangle,
  Check,
  CircleAlert,
  Info,
  X,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useToastContext } from "../hooks/useToast";
import type { Toast } from "../hooks/useToast";

/* ─── 类型 → 图标映射 ────────────────────────────────────── */

const ICON_MAP: Record<Toast["type"], LucideIcon> = {
  success: Check,
  error: CircleAlert,
  info: Info,
  warning: AlertTriangle,
};

/* ─── Toast 组件 ───────────────────────────────────────────── */

function ToastItem({ toast }: { toast: Toast }) {
  const { remove } = useToastContext();
  const Icon = ICON_MAP[toast.type];

  return (
    <div className={`toast toast--${toast.type}`} role="alert">
      <div className="toast__icon">
        <Icon size={16} aria-hidden="true" />
      </div>
      <span className="toast__message">{toast.message}</span>
      <button
        className="toast__close"
        onClick={() => remove(toast.id)}
        aria-label="关闭通知"
        type="button"
      >
        <X size={14} aria-hidden="true" />
      </button>
    </div>
  );
}

/* ─── Toast 容器 ─────────────────────────────────────────────── */

export function ToastContainer() {
  const { toasts } = useToastContext();

  if (toasts.length === 0) return null;

  return (
    <div className="toast-container" aria-live="polite">
      {toasts.map((toast) => (
        <ToastItem key={toast.id} toast={toast} />
      ))}
    </div>
  );
}
