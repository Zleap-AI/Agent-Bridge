/**
 * 权限管理 Hook
 *
 * 管理权限请求和授权决策。
 *
 * @file usePermission.ts
 * @author Lzm
 * @date 2026-07-21
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x
 */

import { useCallback, useEffect, useState } from "react";
import { LocalAdminClient } from "../../shared/api/local";
import type { PermissionRequestEvent } from "../../shared/types";

/**
 * usePermission - 权限管理 Hook
 *
 * @param client  WebSocket 客户端实例
 */
export function usePermission(client: LocalAdminClient) {
  // ── 状态 ──────────────────────────────────────────────────────────────

  /** 当前待处理的权限请求 */
  const [request, setRequest] = useState<PermissionRequestEvent | null>(null);

  /** 授权历史记录（最多保留 50 条） */
  const [history, setHistory] = useState<PermissionRequestEvent[]>([]);

  /** 授权弹窗是否打开 */
  const [dialogOpen, setDialogOpen] = useState(false);

  /** 授权决策正在提交中 */
  const [loading, setLoading] = useState(false);

  // ── 方法 ──────────────────────────────────────────────────────────────

  /**
   * 收到权限请求
   *
   * 由客户端回调触发，更新当前请求并打开弹窗。
   */
  const handleRequest = useCallback((event: PermissionRequestEvent) => {
    console.debug(`[PERM_DEBUG] 收到权限请求: session_id=${event.session_id?.slice(0, 16)}, agent_id=${event.agent_id}, message=${(event.message || "").slice(0, 50)}`);
    setRequest(event);
    setDialogOpen(true);
    setLoading(false);
  }, []);

  /**
   * 关闭弹窗（如果不在提交中）
   */
  const close = useCallback(() => {
    if (!loading) {
      setDialogOpen(false);
      setRequest(null);
    }
  }, [loading]);

  /**
   * 批准当前权限请求
   */
  const allow = useCallback(async () => {
    if (!request) {
      console.warn("[PERM_DEBUG] allow: request 为 null，忽略");
      return;
    }
    console.debug(`[PERM_DEBUG] allow: 开始批准权限, session_id=${request.session_id?.slice(0, 16)}, agent_id=${request.agent_id}`);
    setLoading(true);
    try {
      const params = {
        session_id: request.session_id,
        agent_id: request.agent_id,
        allowed: true,
      };
      console.debug(`[PERM_DEBUG] allow: 发送请求, params=`, JSON.stringify(params));
      const result = await client.request("session/permission_response", params);
      console.debug(`[PERM_DEBUG] allow: 请求成功, result=`, JSON.stringify(result).slice(0, 100));
      setHistory((prev) => {
        const next = [...prev, request];
        return next.length > 50 ? next.slice(-50) : next;
      });
      setDialogOpen(false);
      setRequest(null);
    } catch (e) {
      console.error("[PERM_DEBUG] 允许权限请求失败:", e);
      setLoading(false);
      setDialogOpen(false);
      setRequest(null);
    }
  }, [client, request]);

  /**
   * 拒绝当前权限请求
   */
  const deny = useCallback(async () => {
    if (!request) {
      console.warn("[PERM_DEBUG] deny: request 为 null，忽略");
      return;
    }
    console.debug(`[PERM_DEBUG] deny: 开始拒绝权限, session_id=${request.session_id?.slice(0, 16)}, agent_id=${request.agent_id}`);
    setLoading(true);
    try {
      const params = {
        session_id: request.session_id,
        agent_id: request.agent_id,
        allowed: false,
      };
      console.debug(`[PERM_DEBUG] deny: 发送请求, params=`, JSON.stringify(params));
      const result = await client.request("session/permission_response", params);
      console.debug(`[PERM_DEBUG] deny: 请求成功, result=`, JSON.stringify(result).slice(0, 100));
      setHistory((prev) => {
        const next = [...prev, request];
        return next.length > 50 ? next.slice(-50) : next;
      });
      setDialogOpen(false);
      setRequest(null);
    } catch (e) {
      console.error("[PERM_DEBUG] 拒绝权限请求失败:", e);
      setLoading(false);
      setDialogOpen(false);
      setRequest(null);
    }
  }, [client, request]);

  /**
   * 始终允许（设置 auto_approve 模式）
   */
  const allowAlways = useCallback(async () => {
    if (!request) {
      console.warn("[PERM_DEBUG] allowAlways: request 为 null，忽略");
      return;
    }
    console.debug(`[PERM_DEBUG] allowAlways: 开始始终允许, session_id=${request.session_id?.slice(0, 16)}, agent_id=${request.agent_id}`);
    setLoading(true);
    try {
      const params = {
        session_id: request.session_id,
        agent_id: request.agent_id,
        allowed: true,
        permission_mode: "auto_approve",
      };
      console.debug(`[PERM_DEBUG] allowAlways: 发送请求, params=`, JSON.stringify(params));
      const result = await client.request("session/permission_response", params);
      console.debug(`[PERM_DEBUG] allowAlways: 请求成功, result=`, JSON.stringify(result).slice(0, 100));
      setHistory((prev) => {
        const next = [...prev, request];
        return next.length > 50 ? next.slice(-50) : next;
      });
      setDialogOpen(false);
      setRequest(null);
    } catch (e) {
      console.error("[PERM_DEBUG] 始终允许权限请求失败:", e);
      setLoading(false);
      setDialogOpen(false);
      setRequest(null);
    }
  }, [client, request]);

  // ── 副作用：注册客户端回调 ────────────────────────────────────────────

  useEffect(() => {
    client.onPermissionRequest(handleRequest);
  }, [client, handleRequest]);

  // ── 返回值 ────────────────────────────────────────────────────────────

  return {
    request,
    history,
    dialogOpen,
    loading,
    handleRequest,
    allow,
    deny,
    allowAlways,
    close,
  };
}
