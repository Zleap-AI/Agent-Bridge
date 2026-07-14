import {
  Check,
  ChevronRight,
  Copy,
  LoaderCircle,
  Menu,
  X,
  type LucideIcon,
} from "lucide-react";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { useI18n } from "../i18n";

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  icon?: LucideIcon;
  loading?: boolean;
}

export function Button({ variant = "secondary", icon: Icon, loading, children, className = "", disabled, ...props }: ButtonProps) {
  return (
    <button className={`button button--${variant} ${className}`} disabled={disabled || loading} {...props}>
      {loading ? <LoaderCircle className="spin" size={16} aria-hidden="true" /> : Icon ? <Icon size={16} aria-hidden="true" /> : null}
      <span>{children}</span>
    </button>
  );
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: LucideIcon;
  label: string;
  active?: boolean;
  danger?: boolean;
}

export function IconButton({ icon: Icon, label, active, danger, className = "", ...props }: IconButtonProps) {
  return (
    <button
      className={`icon-button ${active ? "is-active" : ""} ${danger ? "is-danger" : ""} ${className}`}
      aria-label={label}
      title={label}
      {...props}
    >
      <Icon size={18} aria-hidden="true" />
    </button>
  );
}

export function MobileMenuButton({ onClick, expanded }: { onClick: () => void; expanded: boolean }) {
  const { t } = useI18n();
  return <IconButton icon={Menu} label={t("common.mobileMenu")} onClick={onClick} className="mobile-menu-button" aria-expanded={expanded} aria-controls="app-navigation" />;
}

export function Spinner({ label }: { label?: string }) {
  const { t } = useI18n();
  return (
    <div className="spinner-row" role="status">
      <LoaderCircle className="spin" size={18} aria-hidden="true" />
      <span>{label || t("common.loading")}</span>
    </div>
  );
}

export function StatusDot({ status }: { status: "online" | "busy" | "offline" | "error" }) {
  return <span className={`status-dot status-dot--${status}`} aria-hidden="true" />;
}

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  body: string;
  action?: ReactNode;
  compact?: boolean;
}

export function EmptyState({ icon: Icon, title, body, action, compact }: EmptyStateProps) {
  return (
    <div className={`empty-state ${compact ? "empty-state--compact" : ""}`}>
      <div className="empty-state__icon"><Icon size={20} aria-hidden="true" /></div>
      <h2>{title}</h2>
      <p>{body}</p>
      {action ? <div className="empty-state__action">{action}</div> : null}
    </div>
  );
}

export function Notice({ tone = "info", children }: { tone?: "info" | "warning" | "error" | "success"; children: ReactNode }) {
  return <div className={`notice notice--${tone}`} role={tone === "error" ? "alert" : "status"}>{children}</div>;
}

interface DrawerProps {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}

const focusableSelector = [
  "button:not([disabled])",
  "a[href]",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

export function useMobileSidebar(open: boolean, onClose: () => void) {
  const sidebarRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusFirst = () => {
      const sidebar = sidebarRef.current;
      const first = sidebar?.querySelector<HTMLElement>(focusableSelector);
      (first || sidebar)?.focus();
    };
    const timer = window.setTimeout(focusFirst, 0);
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const sidebar = sidebarRef.current;
      if (!sidebar) return;
      const focusable = Array.from(sidebar.querySelectorAll<HTMLElement>(focusableSelector));
      if (!focusable.length) {
        event.preventDefault();
        sidebar.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!sidebar.contains(document.activeElement)) {
        event.preventDefault();
        first.focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener("keydown", handleKeyDown);
      if (previous?.isConnected) previous.focus();
    };
  }, [onClose, open]);

  return sidebarRef;
}

function useModalFocus(open: boolean) {
  const modalRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const timer = window.setTimeout(() => {
      const modal = modalRef.current;
      const initial = modal?.querySelector<HTMLElement>("[data-autofocus]")
        || modal?.querySelector<HTMLElement>(focusableSelector);
      initial?.focus();
    }, 0);
    return () => {
      window.clearTimeout(timer);
      previous?.focus();
    };
  }, [open]);

  return modalRef;
}

function handleModalKeyDown(event: ReactKeyboardEvent<HTMLElement>, onClose: () => void, closeDisabled = false) {
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    if (!closeDisabled) onClose();
    return;
  }
  if (event.key !== "Tab") return;

  const focusable = Array.from(event.currentTarget.querySelectorAll<HTMLElement>(focusableSelector));
  if (focusable.length === 0) {
    event.preventDefault();
    event.currentTarget.focus();
    return;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

export function Drawer({ open, title, description, onClose, children, footer, wide }: DrawerProps) {
  const { t } = useI18n();
  const titleId = useId();
  const modalRef = useModalFocus(open);
  if (!open) return null;
  return (
    <div className="overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <section
        ref={modalRef}
        className={`drawer ${wide ? "drawer--wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={(event) => handleModalKeyDown(event, onClose)}
      >
        <header className="drawer__header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <IconButton icon={X} label={t("common.close")} onClick={onClose} data-autofocus />
        </header>
        <div className="drawer__body">{children}</div>
        {footer ? <footer className="drawer__footer">{footer}</footer> : null}
      </section>
    </div>
  );
}

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  body: string;
  confirmLabel?: string;
  danger?: boolean;
  loading?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({ open, title, body, confirmLabel, danger, loading, onConfirm, onCancel }: ConfirmDialogProps) {
  const { t } = useI18n();
  const titleId = useId();
  const bodyId = useId();
  const modalRef = useModalFocus(open);
  if (!open) return null;
  return (
    <div className="overlay overlay--center" onMouseDown={(event) => { if (event.target === event.currentTarget && !loading) onCancel(); }}>
      <section
        ref={modalRef}
        className="dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        tabIndex={-1}
        onKeyDown={(event) => handleModalKeyDown(event, onCancel, loading)}
      >
        <h2 id={titleId}>{title}</h2>
        <p id={bodyId}>{body}</p>
        <div className="dialog__actions">
          <Button variant="secondary" onClick={onCancel} disabled={loading} data-autofocus>{t("common.cancel")}</Button>
          <Button variant={danger ? "danger" : "primary"} onClick={onConfirm} loading={loading}>{confirmLabel || t("common.confirm")}</Button>
        </div>
      </section>
    </div>
  );
}

export function LanguageControl({ compact = false }: { compact?: boolean }) {
  const { locale, setLocale, t } = useI18n();
  return (
    <label className={compact ? "language-control language-control--compact" : "field"}>
      {!compact ? <span className="field__label">{t("common.language")}</span> : null}
      <select value={locale} onChange={(event) => setLocale(event.target.value as "zh" | "en")} aria-label={t("common.language")}>
        <option value="zh">{t("common.chinese")}</option>
        <option value="en">{t("common.english")}</option>
      </select>
    </label>
  );
}

export function CopyButton({ value, label }: { value: string; label?: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
    } else {
      const input = document.createElement("textarea");
      input.value = value;
      input.style.position = "fixed";
      input.style.opacity = "0";
      document.body.appendChild(input);
      input.select();
      document.execCommand("copy");
      input.remove();
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  };
  return <Button variant="secondary" icon={copied ? Check : Copy} onClick={copy}>{copied ? t("common.copied") : label || t("common.copy")}</Button>;
}

export function ListLink({ icon: Icon, title, body, onClick }: { icon: LucideIcon; title: string; body?: string; onClick: () => void }) {
  return (
    <button className="list-link" onClick={onClick}>
      <span className="list-link__icon"><Icon size={18} aria-hidden="true" /></span>
      <span className="list-link__copy"><strong>{title}</strong>{body ? <small>{body}</small> : null}</span>
      <ChevronRight size={17} aria-hidden="true" />
    </button>
  );
}
