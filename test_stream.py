# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_stream.py
# 测试流式 Prompt 发送（带 _stream 后缀触发流式模式）
#
# Lzm 2026-07-10

import json
import asyncio
import time
import websockets


async def main():
    uri = "ws://localhost:9202/ws/admin"
    print(f"连接 {uri}...")
    async with websockets.connect(uri) as ws:
        print("已连接\n")

        # 1. 创建会话
        req = {
            "jsonrpc": "2.0",
            "id": f"stream_test_session_{int(time.time()*1000)}",
            "method": "invoke",
            "params": {
                "agent_id": "kimi",
                "method": "session/new",
                "params": {}
            }
        }
        print(f">> 创建会话: {json.dumps(req)}")
        await ws.send(json.dumps(req))
        resp = json.loads(await ws.recv())
        print(f"<< 响应: {json.dumps(resp, indent=2)}")
        session_id = resp.get("result", {}).get("sessionId")
        print(f"   Session: {session_id}\n")

        if not session_id:
            print("创建会话失败!")
            return

        # 2. 发送流式 Prompt（ID 含 _stream 触发流式）
        req2 = {
            "jsonrpc": "2.0",
            "id": f"stream_test_prompt_{int(time.time()*1000)}_stream",
            "method": "invoke",
            "params": {
                "agent_id": "kimi",
                "method": "session/prompt",
                "params": {
                    "sessionId": session_id,
                    "prompt": [{"type": "text", "text": "你好，用中文一句话介绍你自己。"}]
                }
            }
        }
        print(f">> 发送 Prompt (流式): {json.dumps(req2)}")
        await ws.send(json.dumps(req2))

        # 3. 读取所有消息（包括流式更新），超时 15 秒
        print("\n开始接收流式消息...")
        start = time.time()
        stream_count = 0
        while time.time() - start < 15:
            try:
                msg = json.loads(await asyncio.wait_for(ws.recv(), timeout=2))
            except asyncio.TimeoutError:
                continue

            if msg.get("method") == "session/update":
                stream_count += 1
                params = msg.get("params", {})
                ctype = params.get("type", "?")
                content = params.get("content", {})
                text = content.get("text", "") if isinstance(content, dict) else str(content)
                print(f"  [{stream_count}] 流式 {ctype}: {text[:150]}")

            elif msg.get("id") and msg["id"].startswith("stream_test_prompt"):
                print(f"\n最终响应: {json.dumps(msg, indent=2)}")

            elif msg.get("id") and msg["id"].startswith("list_"):
                sessions = msg.get("result", [])
                print(f"  会话列表: {len(sessions)} 个")

        print(f"\n共收到 {stream_count} 条流式更新")
        print("测试完成!")


if __name__ == "__main__":
    asyncio.run(main())
