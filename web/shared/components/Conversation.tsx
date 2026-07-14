import { Bot, ChevronDown, FolderOpen, MessageSquare, Plus, RefreshCw, Send } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import type { AgentInfo, MessageInfo, SessionInfo } from "../types";
import { truncateMiddle } from "../format";
import { useI18n } from "../i18n";
import { Button, EmptyState, IconButton, MobileMenuButton, Spinner, StatusDot } from "./ui";

interface ConversationProps {
  agent: AgentInfo | null;
  contextLabel?: string;
  agentControl?: ReactNode;
  sessions: SessionInfo[];
  sessionId: string;
  onSelectSession: (id: string) => void;
  onCreateSession: () => Promise<void> | void;
  onRefreshSessions: () => Promise<void> | void;
  onLoadSession?: () => void;
  sessionsLoading?: boolean;
  messages: MessageInfo[];
  messagesLoading?: boolean;
  sending?: boolean;
  enabled: boolean;
  unavailableTitle?: string;
  unavailableBody?: string;
  onSend: (text: string) => Promise<void> | void;
  onOpenMobileMenu: () => void;
  mobileMenuOpen: boolean;
}

function textOf(message: MessageInfo) {
  return message.content.filter((part) => part.type === "text").map((part) => part.text).join("");
}

export function Conversation({
  agent,
  contextLabel,
  agentControl,
  sessions,
  sessionId,
  onSelectSession,
  onCreateSession,
  onRefreshSessions,
  onLoadSession,
  sessionsLoading,
  messages,
  messagesLoading,
  sending,
  enabled,
  unavailableTitle,
  unavailableBody,
  onSend,
  onOpenMobileMenu,
  mobileMenuOpen,
}: ConversationProps) {
  const { t } = useI18n();
  const [draft, setDraft] = useState("");
  const [announcement, setAnnouncement] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const stickToBottomRef = useRef(true);
  const previousSendingRef = useRef(false);

  useLayoutEffect(() => {
    stickToBottomRef.current = true;
    const container = scrollRef.current;
    if (container) container.scrollTop = container.scrollHeight;
  }, [sessionId]);

  useLayoutEffect(() => {
    if (!stickToBottomRef.current) return;
    const container = scrollRef.current;
    if (container) container.scrollTop = container.scrollHeight;
  }, [messages]);

  useLayoutEffect(() => {
    if (!sending) return;
    stickToBottomRef.current = true;
    const container = scrollRef.current;
    if (container) container.scrollTop = container.scrollHeight;
  }, [sending]);

  useLayoutEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    textarea.style.height = "0px";
    textarea.style.height = `${Math.min(140, Math.max(36, textarea.scrollHeight))}px`;
  }, [draft]);

  useEffect(() => {
    const wasSending = previousSendingRef.current;
    if (sending && !wasSending) setAnnouncement(t("chat.waiting"));
    if (!sending && wasSending) {
      const lastAssistant = [...messages].reverse().find((message) => message.role === "assistant");
      setAnnouncement(t(lastAssistant?.error ? "chat.failed" : "chat.completed"));
    }
    previousSendingRef.current = Boolean(sending);
  }, [messages, sending, t]);

  const submit = async () => {
    const value = draft.trim();
    if (!value || !enabled || sending || messagesLoading || !agent || !sessionId) return;
    setDraft("");
    try { await onSend(value); } catch { setDraft(value); }
    textareaRef.current?.focus();
  };

  return (
    <main className="workspace">
      <header className="workspace__header">
        <div className="workspace__identity">
          <MobileMenuButton onClick={onOpenMobileMenu} expanded={mobileMenuOpen} />
          <div className="agent-avatar"><Bot size={18} aria-hidden="true" /></div>
          <div className="workspace__title">
            <strong>{agent?.displayName || t("remote.chooseAgent")}</strong>
            <span>
              {agent ? <StatusDot status={agent.status === "idle" ? "online" : agent.status === "busy" ? "busy" : agent.status === "error" ? "error" : "offline"} /> : null}
              {contextLabel || (agent ? t(`agent.${agent.status}`) : t("common.offline"))}
            </span>
          </div>
        </div>
        <div className="workspace__controls">
          {agentControl}
          <div className="session-control">
            <select
              value={sessionId}
              onChange={(event) => onSelectSession(event.target.value)}
              disabled={!agent || !enabled || sessionsLoading || messagesLoading || sending}
              aria-label={t("session.title")}
            >
              <option value="">{sessionsLoading ? t("common.loading") : sessions.length ? t("session.select") : t("session.empty")}</option>
              {sessions.map((session) => <option key={session.id} value={session.id}>{truncateMiddle(session.id, 24)}</option>)}
            </select>
            <ChevronDown size={15} aria-hidden="true" />
          </div>
          <IconButton icon={RefreshCw} label={t("common.refresh")} onClick={() => void onRefreshSessions()} disabled={!agent || !enabled || sessionsLoading || messagesLoading || sending} />
          {onLoadSession ? <IconButton icon={FolderOpen} label={t("session.loadExisting")} onClick={onLoadSession} disabled={!agent || !enabled || sessionsLoading || messagesLoading || sending} /> : null}
          <Button
            variant="primary"
            icon={Plus}
            aria-label={t("session.new")}
            onClick={() => void onCreateSession()}
            disabled={!agent || !enabled || sessionsLoading || messagesLoading || sending}
            loading={sessionsLoading}
          >
            {t("session.new")}
          </Button>
        </div>
      </header>

      <div
        className="workspace__messages"
        ref={scrollRef}
        onScroll={(event) => {
          const container = event.currentTarget;
          stickToBottomRef.current = container.scrollHeight - container.scrollTop - container.clientHeight <= 80;
        }}
      >
        {!enabled ? (
          <EmptyState
            icon={MessageSquare}
            title={unavailableTitle || t("common.offline")}
            body={unavailableBody || t("chat.emptyBody")}
          />
        ) : !agent ? (
          <EmptyState icon={Bot} title={t("chat.selectAgent")} body={t("chat.emptyBody")} />
        ) : messagesLoading ? (
          <div className="workspace__center"><Spinner /></div>
        ) : messages.length === 0 ? (
          <EmptyState icon={MessageSquare} title={t("chat.emptyTitle")} body={t("chat.emptyBody")} />
        ) : (
          <div className="message-list">
            {messages.map((message, index) => {
              const text = textOf(message);
              if (message.role === "reasoning") {
                return (
                  <details className="reasoning" key={message.id || index}>
                    <summary>{t("chat.reasoning")}</summary>
                    <div>{text}</div>
                  </details>
                );
              }
              const user = message.role === "user";
              return (
                <article className={`message message--${user ? "user" : "assistant"} ${message.error ? "message--error" : ""}`} key={message.id || index}>
                  {!user ? <div className="message__avatar"><Bot size={16} aria-hidden="true" /></div> : null}
                  <div className="message__content">
                    <span className="message__label">{user ? t("chat.you") : agent.displayName}</span>
                    <div className="message__text">{text || (message.pending ? t("chat.waiting") : "")}{message.pending ? <span className="typing-dot" aria-hidden="true" /> : null}</div>
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </div>

      <footer className="composer-wrap">
        <div className={`composer ${!enabled || !agent || !sessionId || messagesLoading ? "is-disabled" : ""}`}>
          <textarea
            ref={textareaRef}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                if (event.nativeEvent.isComposing || event.keyCode === 229) return;
                event.preventDefault();
                void submit();
              }
            }}
            placeholder={!agent ? t("chat.selectAgent") : !sessionId ? t("chat.selectSession") : t("chat.placeholder")}
            disabled={!enabled || !agent || !sessionId || messagesLoading || sending}
            rows={1}
            aria-label={t("chat.placeholder")}
          />
          <IconButton icon={Send} label={t("chat.send")} onClick={() => void submit()} disabled={!draft.trim() || !enabled || !agent || !sessionId || messagesLoading || sending} className="composer__send" />
        </div>
      </footer>
      <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">{announcement}</div>
    </main>
  );
}
