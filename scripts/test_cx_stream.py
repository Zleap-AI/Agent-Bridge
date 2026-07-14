# -*- coding: utf-8 -*-
"""测试 Codex 流式对话（简化版）
脚本统一管理在 scripts/ 目录下
Lzm 2026-07-10"""

import json
import asyncio
import websockets
import time

async def main():
    uri = "ws://localhost:9202/ws/admin"
    async with websockets.connect(uri) as ws:
        # 1. 创建新会话
        req = {"jsonrpc": "2.0", "id": "cx1", "method": "invoke",
               "params": {"agent_id": "codex", "method": "session/new", "params": {}}}
        await ws.send(json.dumps(req))
        resp = json.loads(await ws.recv())
        session_id = resp.get("result", {}).get("sessionId")
        print(f"Session: {session_id}")

        if not session_id:
            print("创建会话失败")
            return

        # 2. 流式 Prompt
        req2 = {"jsonrpc": "2.0", "id": "cx2", "method": "invoke",
                "params": {"agent_id": "codex", "method": "session/prompt", "stream": True,
                           "params": {"sessionId": session_id,
                                      "prompt": [{"type": "text", "text": "Say 'hello' in one word."}]}}}
        await ws.send(json.dumps(req2))
        print("流式响应:")
        count = 0
        timeout = time.time() + 60
        while time.time() < timeout:
            try:
                msg = json.loads(await asyncio.wait_for(ws.recv(), timeout=5))
            except asyncio.TimeoutError:
                print(f"[超时] 5秒无数据")
                break
            if "error" in msg:
                print(f"[错误] {json.dumps(msg['error'], ensure_ascii=False)[:200]}")
                break
            result = msg.get("result", "")
            if isinstance(result, str):
                if result:
                    count += 1
                    print(f"  [{count}] {result[:80]}")
                if msg.get("jsonrpc") and "id" in msg and msg.get("id", "").startswith("cx2"):
                    break  # 最终响应
            elif isinstance(result, dict):
                text = result.get("text", "") or result.get("content", "")
                if text:
                    count += 1
                    print(f"  [{count}] {text[:80]}")
                if result.get("stopReason"):
                    print(f"[完成] stopReason={result.get('stopReason')}")
                    break
            else:
                print(f"[其他] {str(result)[:100]}")
                break
        print(f"\n共收到 {count} 条消息")

asyncio.run(main())
