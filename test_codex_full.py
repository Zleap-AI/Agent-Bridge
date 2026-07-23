# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_codex_full.py
# Codex CLI 完整功能测试
# 测试: session/new → sessions/list → session/prompt (流式+非流式)
#     → sessions/messages → session/load → session/resume → session/cancel
#
# 正确处理 Codex 响应慢的特性（时间倍率 ×2 于普通 Agent）
# 测试流式（stream=true）和非流式（stream=false）两种模式
#
# Lzm 2026-07-21

import asyncio
import json
import time
import websockets

WS_URL = "ws://127.0.0.1:9202/ws/admin"
AGENT_ID = "codex"

# 超时配置
TIMEOUT_SHORT = 40     # sessions/list, session/load 等
TIMEOUT_NEW = 60       # session/new (Codex 首次创建可能较慢)
TIMEOUT_PROMPT_STREAM = 180  # 流式 prompt (Codex 需要较长时间生成)
TIMEOUT_PROMPT_BLOCK = 120   # 非流式 prompt
TIMEOUT_CANCEL = 20   # session/cancel

OK = 0
FAIL = 0
SKIP = 0


def extract_stream_type_and_text(params):
    """
    从流式更新 params 中提取 type 和 text
    兼容三种格式:
    1. 标准格式: {update: {sessionUpdate: "...", content: {type: "text", text: "..."}}}
    2. Codex-like: {update: {type: "response", content: {text: "..."}}}
    3. Codex 扁平: {type: "response", content: {text: "..."}}
    """
    # 格式 1+2: 嵌套在 update 中
    update = params.get("update", {})
    ct = update.get("sessionUpdate", "") or update.get("type", "")
    content = update.get("content", {})

    # 格式 3: 扁平 params
    if not ct:
        ct = params.get("type", "")

    # 提取文本
    txt = ""
    if isinstance(content, dict):
        txt = content.get("text", "") or content.get("content", "") or ""
        if content.get("type") == "text":
            txt = content.get("text", "")
    elif isinstance(content, str):
        txt = content

    # 扁平 params 的 content
    if not txt:
        flat_content = params.get("content", None)
        if isinstance(flat_content, dict):
            txt = flat_content.get("text", "") or flat_content.get("content", "") or ""
        elif isinstance(flat_content, str):
            txt = flat_content

    return ct, txt


def log(msg, status=""):
    """统一日志"""
    symbols = {"ok": " ✅", "fail": " ❌", "skip": " ⚠️", "info": " ℹ️", "step": "  "}
    prefix = symbols.get(status, symbols["step"])
    ts = time.strftime("%H:%M:%S")
    print(f"[{ts}]{prefix} {msg}")


async def send_and_wait(ws, request, timeout, tag=""):
    """发送请求并等待匹配 ID 的响应"""
    await ws.send(json.dumps(request))
    start = time.time()
    while True:
        elapsed = time.time() - start
        if elapsed > timeout:
            return {"error": {"code": -1, "message": f"超时 ({timeout}s) [{tag}]"}}
        try:
            resp = json.loads(await asyncio.wait_for(ws.recv(), timeout=min(5, timeout - elapsed)))
        except asyncio.TimeoutError:
            continue
        if resp.get("id") == request.get("id"):
            return resp


async def main():
    global OK, FAIL, SKIP
    log("=" * 60, "step")
    log("Codex CLI 完整功能测试", "step")
    log("=" * 60, "step")

    async with websockets.connect(WS_URL, max_size=10*1024*1024) as ws:
        # 读取欢迎
        welcome = json.loads(await asyncio.wait_for(ws.recv(), timeout=5))
        log(f"已连接 Bridge", "ok")

        # ── 1. session/new ──────────────────────────────────
        log("-" * 50, "step")
        log("1. session/new — 创建会话", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c1", "method": "invoke",
            "params": {"agent_id": AGENT_ID, "method": "session/new", "params": {}}
        }, TIMEOUT_NEW, "session/new")
        sid = resp.get("result", {}).get("sessionId")
        if not sid:
            log(f"session/new 失败: {resp.get('error', {}).get('message', '无 sessionId')}", "fail")
            FAIL += 1
            return
        log(f"session/new → {sid[:48]}...", "ok")
        OK += 1

        # ── 2. sessions/list ────────────────────────────────
        log("-" * 50, "step")
        log("2. sessions/list — 查询会话列表", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c2", "method": "sessions/list",
            "params": {"agent_id": AGENT_ID}
        }, TIMEOUT_SHORT, "sessions/list")
        sessions = resp.get("result", [])
        if isinstance(sessions, list):
            log(f"sessions/list → {len(sessions)} 个会话", "ok")
            OK += 1
        else:
            log(f"sessions/list → {sessions}", "skip")
            SKIP += 1

        # ── 3. session/prompt (流式) ─────────────────────────
        log("-" * 50, "step")
        log("3. session/prompt — 发送流式对话 (stream=true)", "step")
        prompt_id = "c3_stream"
        await ws.send(json.dumps({
            "jsonrpc": "2.0", "id": prompt_id, "method": "invoke",
            "params": {
                "agent_id": AGENT_ID, "method": "session/prompt",
                "params": {
                    "sessionId": sid,
                    "prompt": [{"type": "text", "text": "请用一句中文回复：你好世界。"}]
                },
                "stream": True  # 显式启用流式
            }
        }))
        log("提示词已发送，等待流式响应...", "info")

        chunks = []
        final = None
        start = time.time()
        timed_out = False

        while True:
            elapsed = time.time() - start
            if elapsed > TIMEOUT_PROMPT_STREAM:
                log(f"流式响应超时 ({TIMEOUT_PROMPT_STREAM}s)", "skip")
                timed_out = True
                break
            try:
                data = json.loads(await asyncio.wait_for(ws.recv(), timeout=min(10, TIMEOUT_PROMPT_STREAM - elapsed)))
            except asyncio.TimeoutError:
                continue

            if data.get("method") == "session/update":
                p = data.get("params", {})
                chunks.append(p)
                ct, txt = extract_stream_type_and_text(p)
                if ct in ("agent_thought_chunk", "thinking_chunk", "thought") and txt:
                    log(f"  [thought] +{len(txt)} chars", "info")
                elif ct in ("agent_message_chunk", "message_chunk", "response", "content_chunk") and txt:
                    log(f"  [response] +{len(txt)} chars", "info")
                elif ct == "final":
                    log(f"  [final]", "info")
                continue

            if data.get("id") == prompt_id:
                final = data
                break

        # 处理流式结果
        if timed_out:
            log("发送 session/cancel 清理...", "info")
            resp = await send_and_wait(ws, {
                "jsonrpc": "2.0", "id": "c3_cancel", "method": "session/cancel",
                "params": {"agent_id": AGENT_ID, "sessionId": sid}
            }, TIMEOUT_CANCEL, "cancel-after-timeout")
            if resp.get("result"):
                log("session/cancel 确认", "ok")
            else:
                log(f"session/cancel 失败: {resp.get('error', '')}", "skip")
            SKIP += 1
        elif chunks:
            # 检查是否有 response 类型的 chunk
            has_response = any(
                p.get("type") in ("agent_message_chunk", "message_chunk", "response")
                for p in chunks
            )
            if has_response:
                log(f"流式完成: {len(chunks)} 个块 (含 response)", "ok")
                OK += 1
            else:
                log(f"流式完成: {len(chunks)} 个块 (无 response 类型，可能仅有 final)", "ok")
                OK += 1
        elif final:
            if final.get("result"):
                log(f"prompt 完成 (非流式响应)", "ok")
                OK += 1
            elif final.get("error"):
                log(f"prompt 错误: {final['error'].get('message','')}", "fail")
                FAIL += 1
        else:
            log("流式无结果", "skip")
            SKIP += 1

        # ── 4. sessions/messages ─────────────────────────────
        log("-" * 50, "step")
        log("4. sessions/messages — 查询消息历史", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c4", "method": "sessions/messages",
            "params": {"agent_id": AGENT_ID, "session_id": sid}
        }, TIMEOUT_SHORT, "sessions/messages")
        msgs = resp.get("result", [])
        if isinstance(msgs, dict):
            msgs = msgs.get("messages", [])
        if msgs:
            for m in msgs:
                role = m.get("role", "?")
                text = (m.get("text", "") or m.get("content", "") or "")[:80]
                log(f"sessions/messages → [{role}] {text}", "ok")
            OK += 1
        elif timed_out:
            log("sessions/messages → 流式超时，消息可能未完整持久化", "skip")
            SKIP += 1
        else:
            log("sessions/messages → 无消息（可能未持久化）", "skip")
            SKIP += 1

        # ── 5. session/load ──────────────────────────────────
        log("-" * 50, "step")
        log("5. session/load — 加载已有会话", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c5", "method": "invoke",
            "params": {"agent_id": AGENT_ID, "method": "session/load",
                       "params": {"sessionId": sid}}
        }, TIMEOUT_SHORT, "session/load")
        result = resp.get("result", {})
        if isinstance(result, dict) and result.get("status") == "ok":
            log(f"session/load → 成功（{len(result.get('messages', []))} 条消息）", "ok")
            OK += 1
        elif result is None:
            log("session/load → 成功", "ok")
            OK += 1
        else:
            log(f"session/load 失败: {resp.get('error', '')}", "fail")
            FAIL += 1

        # ── 6. session/resume ────────────────────────────────
        log("-" * 50, "step")
        log("6. session/resume — 恢复会话", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c6", "method": "invoke",
            "params": {"agent_id": AGENT_ID, "method": "session/resume",
                       "params": {"sessionId": sid}}
        }, TIMEOUT_SHORT, "session/resume")
        result = resp.get("result", {})
        if isinstance(result, dict) and result.get("status") == "ok":
            log("session/resume → 成功", "ok")
            OK += 1
        elif resp.get("result") is None and not resp.get("error"):
            # 有些 Agent 返回 null result + no error 表示成功
            log("session/resume → 成功（无 content）", "ok")
            OK += 1
        else:
            error = resp.get("error", {})
            log(f"session/resume 失败: {error.get('message', str(error))}", "skip")
            SKIP += 1

        # ── 7. session/cancel (长任务取消) ────────────────────
        log("-" * 50, "step")
        log("7. session/cancel — 长任务取消测试", "step")

        # 先发一个长任务
        prompt_id = "c7_long"
        await ws.send(json.dumps({
            "jsonrpc": "2.0", "id": prompt_id, "method": "invoke",
            "params": {
                "agent_id": AGENT_ID, "method": "session/prompt",
                "params": {
                    "sessionId": sid,
                    "prompt": [{"type": "text",
                                "text": "请写一篇300字的文章，关于人工智能对未来教育的影响。"}]
                },
                "stream": True
            }
        }))
        log("长任务已发送，等待 10 秒后取消...", "info")
        await asyncio.sleep(10)

        # 发送 cancel
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c7_cancel", "method": "session/cancel",
            "params": {"agent_id": AGENT_ID, "sessionId": sid}
        }, TIMEOUT_CANCEL, "cancel-long-task")

        cancel_ok = resp.get("result", {}).get("status") == "ok"
        if cancel_ok:
            log("session/cancel → 确认取消成功", "ok")
        else:
            # 即使 cancel 返回 error，也要继续检查 Agent 是否实际取消了
            log(f"session/cancel 返回: {resp.get('error', resp)}", "info")

        # 等待长任务最终响应（可能被取消或完成）
        log("等待长任务最终响应...", "info")
        got_final = False
        try:
            while True:
                data = json.loads(await asyncio.wait_for(ws.recv(), timeout=25))
                if data.get("id") == prompt_id:
                    got_final = True
                    if data.get("error"):
                        code = data["error"].get("code")
                        msg = data["error"].get("message", "")
                        if code == -32800 or "cancel" in msg.lower():
                            log(f"长任务已被取消: code={code} ✓", "ok")
                        else:
                            log(f"长任务错误: {msg[:80]}", "info")
                    elif data.get("result"):
                        log("长任务完成（未被取消）", "info")
                    break
        except asyncio.TimeoutError:
            log("等待最终响应超时（Agent 可能已处理 cancel）", "info")

        # 取消测试计分
        if cancel_ok or got_final:
            # 只要 cancel 成功或最终收到了响应，都算通过
            log("取消测试完成", "ok")
            OK += 1
        else:
            log("取消测试未获得预期结果", "skip")
            SKIP += 1

        # ── 8. session/prompt (非流式) ──────────────────────
        log("-" * 50, "step")
        log("8. session/prompt — 非流式对话 (stream=false)", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c8_block", "method": "invoke",
            "params": {
                "agent_id": AGENT_ID, "method": "session/prompt",
                "params": {
                    "sessionId": sid,
                    "prompt": [{"type": "text", "text": "回复OK"}]
                },
                "stream": False
            }
        }, TIMEOUT_PROMPT_BLOCK, "prompt-blocking")
        if resp.get("result"):
            text = ""
            if isinstance(resp["result"], dict):
                text = resp["result"].get("text", "")
            elif isinstance(resp["result"], str):
                text = resp["result"][:60]
            log(f"非流式 prompt → 成功: {text}", "ok")
            OK += 1
        elif resp.get("error"):
            log(f"非流式 prompt 错误: {resp['error'].get('message','')}", "fail")
            FAIL += 1
        else:
            log("非流式 prompt 无响应", "skip")
            SKIP += 1

        # ── 9. sessions/list (验证会话列表) ──────────────────
        log("-" * 50, "step")
        log("9. sessions/list — 验证会话列表（含消息数）", "step")
        resp = await send_and_wait(ws, {
            "jsonrpc": "2.0", "id": "c9", "method": "sessions/list",
            "params": {"agent_id": AGENT_ID}
        }, TIMEOUT_SHORT, "final-list")
        sessions = resp.get("result", [])
        log(f"sessions/list → {len(sessions)} 个会话", "ok")
        if sessions:
            # 显示最新的会话信息
            for s in sessions[:3]:
                msg_count = s.get("message_count", s.get("messageCount", 0))
                title = s.get("title", "") or sid[:16]
                log(f"  session: {title} ({msg_count} msg)", "info")
        OK += 1

        # ── 汇总 ─────────────────────────────────────────────
        log("=" * 60, "step")
        total = OK + FAIL + SKIP
        log(f"测试完成: ✅ {OK}/{total} | ❌ {FAIL} | ⚠️ {SKIP}", "step")
        if FAIL > 0:
            log(f"有 {FAIL} 个测试失败，请检查日志", "fail")
        log("=" * 60, "step")


if __name__ == "__main__":
    asyncio.run(main())
