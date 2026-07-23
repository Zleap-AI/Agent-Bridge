/**
 * 消息管理 Hook
 *
 * 管理消息加载、流式发送和打字机效果。
 *
 * @file useMessages.ts
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x
 */

import { useCallback, useRef, useState } from "react";
import { LocalAdminClient } from "../../shared/api/local";
import type { MessageInfo, StreamEvent } from "../../shared/types";

/**
 * useMessages - 消息管理 Hook
 *
 * @param client   WebSocket 客户端实例
 * @param options.onSessionUpdate  流式响应中 session 更新时的回调
 */
export function useMessages(
  client: LocalAdminClient,
  options?: { onSessionUpdate?: (sessionId: string, agentId: string) => void },
) {
  // ── 状态 ──────────────────────────────────────────────────────────────

  /** 消息列表 */
  const [messages, setMessages] = useState<MessageInfo[]>([]);

  /** 加载历史消息中 */
  const [loading, setLoading] = useState(false);

  /** 正在发送消息 */
  const [sending, setSending] = useState(false);

  /** 错误信息 */
  const [error, setError] = useState<string | null>(null);

  /** 自增 generation，用于防止流式响应的竞态 */
  const streamGeneration = useRef(0);

  /** 自增 generation，用于防止历史消息加载的竞态 */
  const messageLoadGeneration = useRef(0);

  /** ref 保持当前 Agent ID */
  const activeAgentIdRef = useRef("");

  /** ref 保持当前 Session ID */
  const sessionIdRef = useRef("");

  // ── 方法 ──────────────────────────────────────────────────────────────

  /**
   * 加载历史消息
   *
   * @param agentId    Agent ID
   * @param sessionId  会话 ID
   */
  const load = useCallback(
    async (agentId: string, sessionId: string) => {
      const generation = ++messageLoadGeneration.current;
      setMessages([]);
      if (!agentId || !sessionId) return;
      setLoading(true);
      setError(null);
      try {
        const next = await client.getMessages(agentId, sessionId);
        if (generation === messageLoadGeneration.current) {
          setMessages(next);
        }
      } catch (err) {
        if (generation === messageLoadGeneration.current) {
          setError(err instanceof Error ? err.message : String(err));
        }
      } finally {
        if (generation === messageLoadGeneration.current) {
          setLoading(false);
        }
      }
    },
    [client],
  );

  /**
   * 发送消息并处理流式更新
   *
   * @param agentId    Agent ID
   * @param sessionId  会话 ID
   * @param text       消息文本
   */
  const send = useCallback(
    async (agentId: string, sessionId: string, text: string) => {
      if (!agentId || !sessionId) return;
      const generation = ++streamGeneration.current;
      activeAgentIdRef.current = agentId;
      sessionIdRef.current = sessionId;
      const stamp = `${Date.now()}`;
      const assistantId = `assistant-${stamp}`;
      const reasoningId = `reasoning-${stamp}`;

      // 添加用户消息和助手占位消息
      setMessages((current) => [
        ...current,
        {
          id: `user-${stamp}`,
          role: "user",
          content: [{ type: "text", text }],
        },
        {
          id: assistantId,
          role: "assistant",
          content: [{ type: "text", text: "" }],
          pending: true,
        },
      ]);
      setSending(true);
      setError(null);

      try {
        await client.streamMessage(
          agentId,
          sessionId,
          text,
          (event: StreamEvent) => {
            // 检查是否仍是最新的流式请求
            if (
              generation !== streamGeneration.current ||
              activeAgentIdRef.current !== agentId
            ) {
              return;
            }

            // session 更新事件需要通知外部
            if (event.type === "session.updated") {
              if (
                event.sessionId &&
                event.sessionId !== sessionIdRef.current
              ) {
                messageLoadGeneration.current += 1;
                sessionIdRef.current = event.sessionId;
                options?.onSessionUpdate?.(event.sessionId, agentId);
              }
              return;
            }

            // 逐条更新消息列表（打字机效果）
            setMessages((current) => {
              if (event.type === "reasoning.delta") {
                const exists = current.some((m) => m.id === reasoningId);
                if (!exists) {
                  const index = current.findIndex(
                    (m) => m.id === assistantId,
                  );
                  const copy = [...current];
                  copy.splice(Math.max(0, index), 0, {
                    id: reasoningId,
                    role: "reasoning",
                    content: [{ type: "text", text: event.text }],
                  });
                  return copy;
                }
                return current.map((m) =>
                  m.id === reasoningId
                    ? {
                        ...m,
                        content: [
                          {
                            type: "text",
                            text: `${m.content[0]?.text || ""}${event.text}`,
                          },
                        ],
                      }
                    : m,
                );
              }
              if (event.type === "message.delta") {
                return current.map((m) =>
                  m.id === assistantId
                    ? {
                        ...m,
                        pending: true,
                        content: [
                          {
                            type: "text",
                            text: `${m.content[0]?.text || ""}${event.text}`,
                          },
                        ],
                      }
                    : m,
                );
              }
              if (event.type === "error") {
                return current.map((m) =>
                  m.id === assistantId
                    ? {
                        ...m,
                        pending: false,
                        error: true,
                        content: [{ type: "text", text: event.message }],
                      }
                    : m,
                );
              }
              if (event.type === "done") {
                return current.map((m) =>
                  m.id === assistantId
                    ? { ...m, pending: false }
                    : m,
                );
              }
              return current;
            });
          },
        );
      } catch (err) {
        if (
          generation !== streamGeneration.current ||
          activeAgentIdRef.current !== agentId
        ) {
          return;
        }
        setMessages((current) =>
          current.map((m) =>
            m.id === assistantId
              ? {
                  ...m,
                  pending: false,
                  error: true,
                  content: [
                    {
                      type: "text",
                      text: err instanceof Error ? err.message : String(err),
                    },
                  ],
                }
              : m,
          ),
        );
      } finally {
        if (
          generation === streamGeneration.current &&
          activeAgentIdRef.current === agentId
        ) {
          setSending(false);
        }
      }
    },
    [client, options],
  );

  /**
   * 取消当前正在发送的消息
   */
  const cancel = useCallback(() => {
    streamGeneration.current += 1;
    setSending(false);
  }, []);

  /**
   * 清空消息列表
   */
  const clear = useCallback(() => {
    messageLoadGeneration.current += 1;
    streamGeneration.current += 1;
    setMessages([]);
    setLoading(false);
    setSending(false);
    setError(null);
  }, []);

  // ── 返回值 ────────────────────────────────────────────────────────────

  return {
    messages,
    loading,
    sending,
    error,
    load,
    send,
    cancel,
    clear,
  };
}
