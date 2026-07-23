/**
 * Agent 管理 Hook
 *
 * 管理 Agent 列表、活动状态、启动/停止操作，并通过 WebSocket 监听状态变化。
 *
 * @file useAgents.ts
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalAdminClient, localApi } from "../../shared/api/local";
import type { AgentInfo } from "../../shared/types";

/**
 * useAgents - Agent 管理 Hook
 *
 * @param client   WebSocket 客户端实例，用于监听 Agent 状态变化
 * @param initialAgentId  初始选中的 Agent ID（可选）
 */
export function useAgents(client: LocalAdminClient, initialAgentId?: string) {
  // ── 状态 ──────────────────────────────────────────────────────────────

  /** Agent 列表 */
  const [agents, setAgents] = useState<AgentInfo[]>([]);

  /** 当前选中的 Agent ID */
  const [activeAgentId, setActiveAgentId] = useState<string | null>(
    initialAgentId ?? null,
  );

  /** 加载中（仅初始加载） */
  const [loading, setLoading] = useState(true);

  /** 错误信息 */
  const [error, setError] = useState<string | null>(null);

  /** 正在进行启动/停止操作的 Agent ID 集合 */
  const [pendingActions, setPendingActions] = useState<Set<string>>(new Set());

  /** ref 保持 activeAgentId 的最新值，避免闭包过期 */
  const activeAgentIdRef = useRef<string | null>(activeAgentId);
  activeAgentIdRef.current = activeAgentId;

  // ── 计算属性 ──────────────────────────────────────────────────────────

  /** 当前选中的 Agent 对象 */
  const activeAgent = useMemo(
    () => agents.find((agent) => agent.id === activeAgentId) || null,
    [agents, activeAgentId],
  );

  // ── 方法 ──────────────────────────────────────────────────────────────

  /**
   * 刷新 Agent 列表
   * 从服务端拉取最新的 LocalStatus 数据，更新 agents 列表并校正 activeAgentId。
   */
  const refresh = useCallback(async () => {
    try {
      setError(null);
      const next = await localApi.getStatus();
      setAgents(next.agents);
      setActiveAgentId((current) =>
        current && next.agents.some((agent) => agent.id === current)
          ? current
          : next.agents[0]?.id ?? null,
      );
    } catch (err) {
      setError(
        err instanceof Error ? err.message : String(err),
      );
    }
  }, []);

  /**
   * 启动指定 Agent
   * 向服务端发送启动请求，操作完成后自动刷新列表。
   */
  const startAgent = useCallback(
    async (id: string) => {
      if (pendingActions.has(id)) return;
      setPendingActions((prev) => new Set(prev).add(id));
      try {
        await localApi.startAgent(id);
        await refresh();
      } catch (err) {
        const agent = agents.find((a) => a.id === id);
        const name = agent?.displayName || id;
        setError(
          `启动 Agent "${name}" 失败: ${
            err instanceof Error ? err.message : String(err)
          }`,
        );
      } finally {
        setPendingActions((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
      }
    },
    [agents, pendingActions, refresh],
  );

  /**
   * 停止指定 Agent
   * 向服务端发送停止请求，操作完成后自动刷新列表。
   */
  const stopAgent = useCallback(
    async (id: string) => {
      if (pendingActions.has(id)) return;
      setPendingActions((prev) => new Set(prev).add(id));
      try {
        await localApi.stopAgent(id);
        await refresh();
      } catch (err) {
        const agent = agents.find((a) => a.id === id);
        const name = agent?.displayName || id;
        setError(
          `停止 Agent "${name}" 失败: ${
            err instanceof Error ? err.message : String(err)
          }`,
        );
      } finally {
        setPendingActions((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
      }
    },
    [agents, pendingActions, refresh],
  );

  /**
   * 切换选中的 Agent
   */
  const setActiveAgent = useCallback((id: string) => {
    setActiveAgentId(id);
  }, []);

  // ── 副作用：初始化 + 监听 + 轮询 ─────────────────────────────────────

  useEffect(() => {
    // 监听 WebSocket 推送的 Agent 变化
    const offAgents = client.onAgents((next) => {
      setAgents(next);
      setActiveAgentId((current) =>
        current && next.some((agent) => agent.id === current)
          ? current
          : next[0]?.id ?? null,
      );
    });

    // 监听连接状态变化，重新连接后自动刷新
    const offConnection = client.onConnection(() => {
      void refresh();
    });

    // 初始加载 Agent 列表
    void refresh().finally(() => setLoading(false));

    // 定时轮询，保持状态同步（5 秒间隔）
    const timer = window.setInterval(() => void refresh(), 5000);

    return () => {
      window.clearInterval(timer);
      offAgents();
      offConnection();
    };
  }, [client, refresh]);

  // ── 返回值 ────────────────────────────────────────────────────────────

  return {
    agents,
    activeAgentId,
    setActiveAgentId,
    activeAgent,
    loading,
    error,
    pendingActions,
    refresh,
    startAgent,
    stopAgent,
    setActiveAgent,
  };
}
