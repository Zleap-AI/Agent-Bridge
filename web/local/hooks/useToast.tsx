/* ============================================================
 * Toast 通知状态管理 Hook
 *
 * 基于 React Context 的全局 Toast 通知系统。
 * 支持 success / error / info / warning 四种类型。
 *
 * @author Lzm
 * @date 2026-07-21
 * @encoding UTF-8
 * @lang TypeScript 5.x (JSX)
 * ============================================================ */

import {
  createContext,
  useCallback,
  useContext,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface Toast {
  id: string;
  type: "success" | "error" | "info" | "warning";
  message: string;
  duration: number; // 自动消失时间（ms），0 表示不自动消失
}

interface ToastOptions {
  duration?: number;
}

interface ToastActions {
  success: (message: string, opts?: ToastOptions) => void;
  error: (message: string, opts?: ToastOptions) => void;
  info: (message: string, opts?: ToastOptions) => void;
  warning: (message: string, opts?: ToastOptions) => void;
}

interface ToastContextValue {
  toasts: Toast[];
  toast: ToastActions;
  remove: (id: string) => void;
}

/* ─── 默认 Duration ────────────────────────────────────────── */

const DEFAULT_DURATION: Record<Toast["type"], number> = {
  success: 3000,
  info: 3000,
  error: 0, // 手动关闭
  warning: 0, // 手动关闭
};

/* ─── Context ───────────────────────────────────────────────── */

const ToastContext = createContext<ToastContextValue | null>(null);

/* ─── Provider ──────────────────────────────────────────────── */

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const idPrefix = useId();
  const counterRef = useRef(0);
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  const remove = useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
    const timer = timersRef.current.get(id);
    if (timer) {
      clearTimeout(timer);
      timersRef.current.delete(id);
    }
  }, []);

  const add = useCallback(
    (type: Toast["type"], message: string, opts?: ToastOptions) => {
      const id = `${idPrefix}-toast-${++counterRef.current}`;
      const duration = opts?.duration ?? DEFAULT_DURATION[type];
      const toast: Toast = { id, type, message, duration };

      setToasts((prev) => [...prev, toast]);

      if (duration > 0) {
        const timer = setTimeout(() => remove(id), duration);
        timersRef.current.set(id, timer);
      }
    },
    [idPrefix, remove],
  );

  const toast: ToastActions = {
    success: useCallback((message, opts?) => add("success", message, opts), [add]),
    error: useCallback((message, opts?) => add("error", message, opts), [add]),
    info: useCallback((message, opts?) => add("info", message, opts), [add]),
    warning: useCallback((message, opts?) => add("warning", message, opts), [add]),
  };

  return (
    <ToastContext.Provider value={{ toasts, toast, remove }}>
      {children}
    </ToastContext.Provider>
  );
}

/* ─── Hook ──────────────────────────────────────────────────── */

export function useToastContext(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    throw new Error("useToastContext must be used within a <ToastProvider>");
  }
  return ctx;
}
