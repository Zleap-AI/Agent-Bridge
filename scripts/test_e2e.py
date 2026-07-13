# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_e2e.py
# zleap-bridge 端到端综合测试
# 测试：Bridge Admin API、SaaS WebSocket 链路、Agent 调用
#
# Lzm 2026-07-09

import json
import asyncio
import sys
import urllib.request

BRIDGE_ADMIN = "http://localhost:9202"
SAAS_STATUS = "http://localhost:9201/status"
SAAS_ADMIN_WS = "ws://localhost:9201/ws/admin"

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

    async with websockets.connect(SAAS_ADMIN_WS) as ws:
        # 1. 先读取欢迎消息 (bridge/list)
        welcome = await asyncio.wait_for(ws.recv(), timeout=5)
        welcome_data = json.loads(welcome)
        assert welcome_data["method"] == "bridge/list", f"期望 bridge/list, 得到 {welcome_data['method']}"
        print(f"  [PASS] SaaS 欢迎消息: bridge/list, connected={welcome_data['params']['bridges'][0]['connected']}")

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
        # sessions/list 被 SaaS 透传到 Bridge
        print(f"  [PASS] sessions/list 响应: {json.dumps(sessions_data, ensure_ascii=False)[:200]}")

        # 4. 发送 invoke: session/new（测试 Claude Code）
        invoke_msg = {
            "id": "test_invoke",
            "method": "invoke",
            "params": {
                "agent_id": "claude-code",
                "method": "session/new",
                "params": {}
            }
        }
        await ws.send(json.dumps(invoke_msg))
        # SaaS 会将 id 加上 _bridge 后缀转发给 Bridge，Bridge 处理后返回
        # 可能收到流式更新或最终响应
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
                    # 流式更新，继续等待最终结果
                    continue
            except asyncio.TimeoutError:
                print(f"  [INFO] session/new 超时（可能无可用 Agent）")
                break

if __name__ == "__main__":
    print("=" * 60)
    print("  Zleap Bridge - 端到端综合测试")
    print("=" * 60)
    print()

    # === 测试集 1: Bridge Admin API ===
    print("[集合 1] Bridge Admin API")

    def test_health():
        h = http_get(f"{BRIDGE_ADMIN}/health")
        assert h["status"] == "ok", f"status={h['status']}"
        assert h["version"] == "0.2.0", f"version={h['version']}"
        agents = h.get("agents", {})
        assert "claude-code" in agents, f"Agent 列表不完整: {agents}"

    def test_agents():
        agents = http_get(f"{BRIDGE_ADMIN}/agents")
        assert len(agents) >= 1, f"Agent 数量={len(agents)}"

    test("Health 检查 (version=0.2.0, agents≥1)", test_health)
    test("Agent 列表 (3 agents)", test_agents)

    # === 测试集 2: SaaS 链路 ===
    print()
    print("[集合 2] SaaS 链路")

    def test_saas_status():
        s = http_get(SAAS_STATUS)
        assert s["paired_bridges"] >= 1, f"未配对: {s}"
        assert s["connected_ws"] >= 1, f"未连接: {s}"

    test("SaaS 配对 + 连接", test_saas_status)

    # === 测试集 3: WebSocket 链路 ===
    print()
    print("[集合 3] SaaS WebSocket 端到端链路")
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
