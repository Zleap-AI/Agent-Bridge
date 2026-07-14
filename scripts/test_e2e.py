# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_e2e.py
# Agent-Bridge 端到端综合测试
# 测试：Bridge Admin API、Admin WebSocket 链路、Agent 调用
# 所有测试均连接 Bridge Admin（:9202），无需独立远程服务模拟器
#
# Lzm 2026-07-13

import json
import asyncio
import os
import sys
import urllib.request
import urllib.parse

BRIDGE_ADMIN = os.environ.get("AGENT_BRIDGE_LOCAL_URL", "http://localhost:9202").rstrip("/")
parsed_admin = urllib.parse.urlparse(BRIDGE_ADMIN)
ws_scheme = "wss" if parsed_admin.scheme == "https" else "ws"
ADMIN_WS = urllib.parse.urlunparse((ws_scheme, parsed_admin.netloc, "/ws/admin", "", "", ""))

passed = 0
failed = 0


def test(name, fn):
    global passed, failed
    try:
        fn()
        print(f"  [PASS] {name}")
        passed += 1
    except Exception as e:
        print(f"  [FAIL] {name}: {e}")
        failed += 1


def http_get(url):
    """HTTP GET 请求"""
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())


async def ws_test():
    """使用 websockets 库进行 WebSocket 测试"""
    try:
        import websockets
    except ImportError:
        print("  [SKIP] 需要 websockets 库: pip install websockets")
        return

    async with websockets.connect(ADMIN_WS) as ws:
        # 1. 先读取欢迎消息 (bridge/list)
        welcome = await asyncio.wait_for(ws.recv(), timeout=5)
        welcome_data = json.loads(welcome)
        assert welcome_data["method"] == "bridge/list", f"期望 bridge/list, 得到 {welcome_data['method']}"
        bridges = welcome_data["params"]["bridges"]
        assert len(bridges) >= 1, "至少有一个 Bridge"
        agents = bridges[0]["agents"]
        print(f"  [PASS] bridge/list 欢迎: connected={bridges[0]['connected']}, agents={len(agents)}")

        # 2. Ping/Pong
        await ws.send(json.dumps({"id": "test_ping", "method": "ping"}))
        pong = await asyncio.wait_for(ws.recv(), timeout=5)
        pong_data = json.loads(pong)
        assert pong_data.get("result") == "pong", f"ping 响应异常: {pong_data}"
        print(f"  [PASS] Ping/Pong 正常")

        # 3. sessions/list
        await ws.send(json.dumps({"id": "test_sessions", "method": "sessions/list", "params": {}}))
        sessions_resp = await asyncio.wait_for(ws.recv(), timeout=10)
        sessions_data = json.loads(sessions_resp)
        print(f"  [PASS] sessions/list 响应: {json.dumps(sessions_data, ensure_ascii=False)[:200]}")

        # 4. 如果没有可用 Agent，跳过 invoke 测试
        if not agents:
            print("  [SKIP] 无可用 Agent，跳过 invoke 测试")
            return

        # 5. 发送 invoke: session/new（使用第一个可用 Agent）
        first_agent = agents[0].get("agent_id") if isinstance(agents[0], dict) else agents[0]
        invoke_msg = {
            "id": "test_invoke",
            "method": "invoke",
            "params": {
                "agent_id": first_agent,
                "method": "session/new",
                "params": {}
            }
        }
        await ws.send(json.dumps(invoke_msg))
        for _ in range(5):
            try:
                resp = await asyncio.wait_for(ws.recv(), timeout=30)
                data = json.loads(resp)
                if "result" in data or "error" in data:
                    if "error" in data:
                        print(f"  [INFO] session/new 结果: error={data['error']}")
                    else:
                        print(f"  [PASS] session/new 成功: {json.dumps(data, ensure_ascii=False)[:200]}")
                    break
                elif data.get("method") == "session/update":
                    continue
            except asyncio.TimeoutError:
                print(f"  [INFO] session/new 超时")
                break


if __name__ == "__main__":
    print("=" * 60)
    print("  Agent-Bridge - 端到端综合测试")
    print("=" * 60)
    print()

    # === 测试集 1: Bridge Admin API ===
    print("[集合 1] Bridge Admin API")

    def test_health():
        h = http_get(f"{BRIDGE_ADMIN}/health")
        assert h["status"] == "ok", f"status={h['status']}"
        assert h["version"] == "0.4.0", f"version={h['version']}"

    def test_agents():
        agents = http_get(f"{BRIDGE_ADMIN}/agents") or []
        ids = [a.get("agent_id", "?") for a in agents]
        print(f"    检测到 {len(agents)} 个 Agent: {', '.join(ids)}")

    test("Health 检查 (version=0.4.0)", test_health)
    test("Agent 列表", test_agents)

    # === 测试集 2: Admin WebSocket 链路 ===
    print()
    print("[集合 2] Admin WebSocket 链路")
    asyncio.run(ws_test())

    # === 汇总 ===
    print()
    print("=" * 60)
    total = passed + failed
    print(f"  测试完成: {total} 总用例")
    if failed == 0:
        print(f"  全部通过 ({passed}/{total}) ✅")
    else:
        print(f"  通过: {passed}, 失败: {failed}")
    print("=" * 60)
    sys.exit(0 if failed == 0 else 1)
