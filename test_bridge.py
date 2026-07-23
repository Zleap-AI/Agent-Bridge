# -*- coding: utf-8 -*-
# Python 3.12+
# test_bridge.py - 桥接器测试脚本 v2
# Lzm 2026-07-22

import urllib.request
import json

BASE = "http://127.0.0.1:9202"

def get(path):
    with urllib.request.urlopen(f"{BASE}{path}") as resp:
        return resp.status, resp.read()

print("=== 1. Agent 列表 ===")
status, data = get("/agents")
print(f"  status={status}")
agents = json.loads(data)
for a in agents:
    print(f"  {a['agent_id']}: {a['display_name']} ({a['status']})")

print("\n=== 2. /api/sessions (legacy) ===")
status, data = get("/api/sessions")
print(f"  status={status}")
if data.strip():
    sessions = json.loads(data)
    print(f"  共 {len(sessions)} 个会话")
    for s in sessions[:5]:
        if isinstance(s, dict):
            sid = s.get('session_id', '')[:16]
            print(f"    [{sid}...] {str(s.get('title', ''))[:40]} | msg:{s.get('message_count', 0)}")
    if len(sessions) > 5:
        print(f"    ... 还有 {len(sessions)-5} 个")
else:
    print("  (empty)")

print("\n=== 3. API v1 local status ===")
status, data = get("/api/v1/local/status")
print(f"  status={status}")
print(f"  body: {data[:300]}")

print("\n=== 测试完成 ===")
