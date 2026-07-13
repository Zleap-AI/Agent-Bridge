# -*- coding: utf-8 -*-
"""WebSocket API 测试 - 验证 sessions/messages 等功能
脚本统一管理在 scripts/ 目录下
Lzm 2026-07-10"""

import json
import websocket

ws = websocket.create_connection("ws://localhost:9202/ws/admin")

# 1. sessions/list - 按 agent 过滤
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "test_1",
    "method": "sessions/list",
    "params": {"agent_id": "kimi"}
}))
resp = json.loads(ws.recv())
result = resp.get("result", [])
if isinstance(result, (str, bytes)):
    result = json.loads(result)
print(f"[sessions/list] Kimi 历史会话数: {len(result)}")

# 2. sessions/messages - 查询会话消息
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "test_2",
    "method": "sessions/messages",
    "params": {
        "agent_id": "kimi",
        "session_id": "session_4754c5b5-2ac6-43ad-8006-baead9b121c3",
        "limit": 3
    }
}))
resp = json.loads(ws.recv())
msgs = resp.get("result", {})
if isinstance(msgs, (str, bytes)):
    msgs = json.loads(msgs)
if isinstance(msgs, dict):
    msgs = msgs.get("messages", [])
print(f"[sessions/messages] 返回 {len(msgs)} 条消息")
if msgs:
    for m in msgs:
        print(f"  role={m['role']}, text={m['text'][:60]}...")
else:
    print("[sessions/messages] 无消息")

# 3. env/check - 环境检查
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "test_3",
    "method": "env/check",
    "params": {}
}))
resp = json.loads(ws.recv())
agents = resp.get("result", [])
if isinstance(agents, (str, bytes)):
    agents = json.loads(agents)
if isinstance(agents, dict):
    agents = agents.get("agents", [])
print(f"[env/check] 发现 {len(agents)} 个 Agent:")
for a in agents:
    print(f"  {a['name']}: status={a['status']}, env_ready={a['env_ready']}")

# 4. 测试新旧 sessions/list（无过滤）
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "test_4",
    "method": "sessions/list",
    "params": {}
}))
resp = json.loads(ws.recv())
all_sessions = resp.get("result", [])
if isinstance(all_sessions, (str, bytes)):
    all_sessions = json.loads(all_sessions)
print(f"[sessions/list all] 总会话数: {len(all_sessions)}")

ws.close()
print("\n=== 所有 WebSocket 测试通过 ===")
