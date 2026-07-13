# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_bridge_api.py
# zleap-bridge WebSocket API 完整功能测试
# 脚本统一管理在 scripts/ 目录下
# 测试: health → agents → session/new → sessions/list → session/prompt
#
# Lzm 2026-07-10

import json
import sys
import asyncio
import time

try:
    import websockets
except ImportError:
    print("请先安装 websockets: py -3.12 -m pip install -i https://mirrors.aliyun.com/pypi/simple/ websockets")
    sys.exit(1)


class BridgeTester:
    """zleap-bridge WebSocket API 测试器"""

    def __init__(self, uri="ws://localhost:9202/ws/admin"):
        self.uri = uri
        self.ws = None
        self.pending = {}

    async def connect(self):
        print(f"\n{'='*60}")
        print(f"📡 连接 Bridge: {self.uri}")
        print(f"{'='*60}")
        self.ws = await websockets.connect(self.uri)
        print("✅ WebSocket 已连接")
        return True

    async def close(self):
        if self.ws:
            await self.ws.close()
            print("🔌 WebSocket 已断开")

    async def send_request(self, method: str, params: dict = None, tag: str = "") -> dict:
        """发送请求并等待响应"""
        msg_id = f"test_{int(time.time()*1000)}_{tag}"
        request = {
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": method,
        }
        if params:
            request["params"] = params

        print(f"\n▶ 发送 [{method}]: {json.dumps(request, ensure_ascii=False)}")
        await self.ws.send(json.dumps(request))

        # 等待匹配的响应
        while True:
            resp = json.loads(await self.ws.recv())
            if resp.get("id") == msg_id:
                print(f"◀ 响应 [{method}]: {json.dumps(resp, ensure_ascii=False, indent=2)}")
                return resp
            # 流式通知显示但不阻塞
            if resp.get("method") == "session/update":
                print(f"  ◀ [流式] {resp.get('params', {}).get('type', '?')}: "
                      f"{resp.get('params', {}).get('content', {}).get('text', '')[:80]}")
            else:
                print(f"  ◀ [其他] {json.dumps(resp, ensure_ascii=False)[:120]}")

    async def test_health(self):
        """测试 bridge/health"""
        print(f"\n{'='*60}")
        print("📋 测试 1: bridge/health")
        print(f"{'='*60}")
        resp = await self.send_request("bridge/health", tag="health")
        assert resp.get("result") is not None, "health 应该返回结果"
        print("✅ bridge/health 通过")
        return resp["result"]

    async def test_agents(self):
        """测试 admin/agents"""
        print(f"\n{'='*60}")
        print("📋 测试 2: admin/agents")
        print(f"{'='*60}")
        resp = await self.send_request("admin/agents", tag="agents")
        assert resp.get("result") is not None, "agents 应该返回结果"
        agents = resp["result"]
        print(f"  发现 {len(agents)} 个 Agent:")
        for a in agents:
            print(f"    - {a.get('agent_id')} ({a.get('display_name')}) status={a.get('status')}")
        assert len(agents) > 0, "至少应该发现 1 个 Agent"
        print("✅ admin/agents 通过")
        return agents

    async def test_new_session(self, agent_id: str):
        """测试 invoke → session/new"""
        print(f"\n{'='*60}")
        print(f"📋 测试 3: invoke → session/new (agent={agent_id})")
        print(f"{'='*60}")
        resp = await self.send_request("invoke", {
            "agent_id": agent_id,
            "method": "session/new",
            "params": {}
        }, tag="session_new")
        assert resp.get("result") is not None, "session/new 应该返回结果"
        session_id = resp["result"].get("sessionId")
        assert session_id, "应该返回 sessionId"
        print(f"✅ session/new 通过: sessionId={session_id}")
        return session_id

    async def test_list_sessions(self):
        """测试 sessions/list"""
        print(f"\n{'='*60}")
        print("📋 测试 4: sessions/list")
        print(f"{'='*60}")
        resp = await self.send_request("sessions/list", tag="list")
        assert resp.get("result") is not None, "sessions/list 应该返回结果"
        sessions = resp["result"]
        print(f"  当前 {len(sessions)} 个活跃会话:")
        for s in sessions:
            print(f"    - agent={s.get('agent_id')} session={s.get('session_id')}")
        print("✅ sessions/list 通过")
        return sessions

    async def test_prompt(self, agent_id: str, session_id: str, text: str):
        """测试 invoke → session/prompt"""
        print(f"\n{'='*60}")
        print(f"📋 测试 5: invoke → session/prompt (agent={agent_id}, session={session_id[:24]}...)")
        print(f"{'='*60}")
        resp = await self.send_request("invoke", {
            "agent_id": agent_id,
            "method": "session/prompt",
            "params": {
                "sessionId": session_id,
                "prompt": [{"type": "text", "text": text}]
            }
        }, tag="prompt")

        # prompt 可能是流式或非流式
        if resp.get("result"):
            result_text = resp["result"].get("text", "") or resp["result"].get("content", "") or str(resp["result"])
            print(f"✅ Prompt 完成 (非流式): {result_text[:200]}")
        elif resp.get("error"):
            print(f"❌ Prompt 失败: {resp['error']}")
            return False
        else:
            # 流式结果已通过 update 消息推送
            print("✅ Prompt 已发送 (流式)")
        return True


async def main():
    tester = BridgeTester()

    try:
        # 1. 连接
        await tester.connect()

        # 2. Health
        await tester.test_health()

        # 3. Agent 列表
        agents = await tester.test_agents()
        if not agents:
            print("❌ 没有可用 Agent，退出测试")
            return

        # 使用第一个可用 Agent
        agent_id = agents[0]["agent_id"]
        print(f"\n👉 使用 Agent: {agent_id}")

        # 4. 创建会话
        session_id = await tester.test_new_session(agent_id)

        # 5. 列出会话
        await tester.test_list_sessions()

        # 6. 发送 Prompt
        await tester.test_prompt(agent_id, session_id, "你好，请用一句话介绍一下你自己。")

        # 等待流式消息收尾
        print("\n等待流式消息...")
        await asyncio.sleep(3)

        print(f"\n{'='*60}")
        print("✅ 全部测试完成!")
        print(f"{'='*60}")

    except Exception as e:
        print(f"\n❌ 测试异常: {e}")
        import traceback
        traceback.print_exc()
    finally:
        await tester.close()


if __name__ == "__main__":
    asyncio.run(main())
