/**
 * Session 管理 Hook
 *
 * 管理会话列表和当前会话切换。
 *
 * @file useSessions.ts
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { LocalAdminClient } from "../../shared/api/local";
import type { SessionInfo } from "../../shared/types";

/**
 * useSessions - 会话管理 Hook
 *
 * @param client   WebSocket 客户端实例
 * @param agentId  当前选中的 Agent ID（可选），变化时自动加载会话列表
 */
export function useSessions(client: LocalAdminClient, agentId?: string | null) {
  // ── 状态 ──────────────────────────────────────────────────────────────

  /** 会话列表 */
  const [sessions, setSessions] = useState<SessionInfo[]>([]);

  /** 当前选中的会话 ID */
  const [sessionId, setSessionId] = useState("");

  /** 加载会话列表中 */
  const [loading, setLoading] = useState(false);

  /** 正在创建新会话 */
  const [creating, setCreating] = useState(false);

  /** 错误信息 */
  const [error, setError] = useState<string | null>(null);

  /** 自增 generation，用于防止竞态 */
  const sessionLoadGeneration = useRef(0);

  /** ref 保持 agentId 的最新值 */
  const agentIdRef = useRef<string | null>(agentId ?? null);
  agentIdRef.current = agentId ?? null;

  // ── 方法 ──────────────────────────────────────────────────────────────

  /**
   * 刷新会话列表
   *
   * @param id  Agent ID
   */
  const refresh = useCallback(
    async (id: string) => {
      const generation = ++sessionLoadGeneration.current;
      if (!id || !client.connected) {
        setSessions([]);
        setSessionId("");
        setLoading(false);
        return;
      }
      setLoading(true);
      setError(null);
      try {
        const next = await client.listSessions(id);
        if (generation !== sessionLoadGeneration.current || agentIdRef.current !== id) return;
        // 去重：相同 session.id 只保留最新一条
        const unique = Array.from(
          new Map(next.map((session) => [session.id, session])).values(),
        );
        setSessions(unique);
        setSessionId((current) =>
          unique.some((session) => session.id === current)
            ? current
            : unique[0]?.id || "",
        );
      } catch (err) {
        if (generation !== sessionLoadGeneration.current || agentIdRef.current !== id) return;
        setSessions([]);
        setSessionId("");
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (generation === sessionLoadGeneration.current && agentIdRef.current === id) {
          setLoading(false);
        }
      }
    },
    [client],
  );

  /**
   * 创建新会话
   *
   * @param id              Agent ID
   * @param cwd             工作目录（可选）
   * @param permissionMode  授权模式（可选）
   * @returns 新会话 ID
   */
  const create = useCallback(
    async (id: string, cwd?: string, permissionMode?: string): Promise<string> => {
      setCreating(true);
      setError(null);
      try {
        const { sessionId: newId } = await client.createSession(id, cwd, permissionMode);
        setSessions((prev) => [
          { id: newId, agentId: id },
          ...prev.filter((session) => session.agentId !== id),
        ]);
        setSessionId(newId);
        return newId;
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setError(msg);
        throw err;
      } finally {
        setCreating(false);
      }
    },
    [client],
  );

  /**
   * 切换当前选中的会话
   *
   * @param id  会话 ID
   */
  const select = useCallback((id: string) => {
    setSessionId(id);
  }, []);

  /**
   * 加载已有会话（按会话 ID 插入列表并选中）
   *
   * 模态框交互逻辑由组件控制，本 hook 只负责更新列表和切换。
   *
   * @param id          Agent ID
   * @param existingId  已有会话 ID
   */
  const loadExisting = useCallback(async (id: string, existingId: string) => {
    setError(null);
    try {
      setSessions((current) => {
        if (current.some((session) => session.id === existingId)) return current;
        return [{ id: existingId, agentId: id }, ...current];
      });
      setSessionId(existingId);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  // ── 副作用：agentId 变化时自动加载 ────────────────────────────────────

  useEffect(() => {
    if (agentId) {
      void refresh(agentId);
    } else {
      setSessions([]);
      setSessionId("");
    }
  }, [agentId, refresh]);

  // ── 返回值 ────────────────────────────────────────────────────────────

  return {
    sessions,
    sessionId,
    loading,
    creating,
    error,
    refresh,
    create,
    select,
    loadExisting,
    setSessionId,
  };
}
