# -*- coding: utf-8 -*-
# Python 3.12+
# test_pi_bridge.py - 通过 bridge Admin WebSocket 测试 pi Agent
# Lzm 2026-07-14

import json, sys, asyncio, time

try:
    import websockets
except ImportError:
    import subprocess
    subprocess.check_call([
        sys.executable, "-m", "pip", "install",
        "-i", "https://mirrors.aliyun.com/pypi/simple/",
        "websockets"
    ])
    import websockets

WS_URL = "ws://localhost:9202/ws/admin"
TIMEOUT = 120  # prompt 可能需要更长时间

async def ws_call_simple(msg, timeout=TIMEOUT):
    """简单请求：一个请求一个响应"""
    async with websockets.connect(WS_URL) as ws:
        await ws.recv()  # 消费欢迎消息
        await ws.send(json.dumps(msg))
        resp = await asyncio.wait_for(ws.recv(), timeout=timeout)
        return json.loads(resp)

async def ws_call_streaming(msg, timeout=TIMEOUT):
    """流式请求：一个请求，持续接收直到 final/error"""
    async with websockets.connect(WS_URL) as ws:
        await ws.recv()  # 消费欢迎消息
        await ws.send(json.dumps(msg))
        
        chunks = []
        thought_parts = []
        response_parts = []
        final_result = None
        
        while True:
            try:
                resp = await asyncio.wait_for(ws.recv(), timeout=timeout)
                data = json.loads(resp)
                
                # 流式更新（bridge 使用 session/update 方法名）
                if data.get("method") == "session/update":
                    params = data.get("params", {})
                    chunk_type = params.get("type", "")
                    # content 可能是 JSON 字符串或已解析对象
                    content = params.get("content", params.get("text", ""))
                    if isinstance(content, str):
                        try:
                            content_obj = json.loads(content)
                            text = content_obj.get("text", content)
                        except:
                            text = content
                    else:
                        text = content.get("text", "") if isinstance(content, dict) else str(content)
                    
                    if chunk_type == "response":
                        response_parts.append(text)
                    elif chunk_type == "thought":
                        thought_parts.append(text)
                    elif chunk_type == "final":
                        # 流式结束
                        break
                    elif chunk_type == "error":
                        return {"error": data.get("params", {}), "chunks": chunks}
                # 普通响应（非流式）
                elif "result" in data or "error" in data:
                    return data
                    
            except asyncio.TimeoutError:
                return {"error": {"message": "流式响应超时"}, "chunks": chunks}
        
        # 合并结果
        result_text = ""
        if thought_parts:
            result_text += "[思考] " + "".join(thought_parts) + "\n"
        if response_parts:
            result_text += "".join(response_parts)
        
        return {"result": [{"type": "text", "text": result_text}], "thought": "".join(thought_parts), "response": "".join(response_parts)}

async def main():
    print("=" * 60)
    print("pi Agent Bridge 测试")
    print("=" * 60)

    # 1. admin/agents
    print("\n1. admin/agents")
    an = await ws_call_simple({"method": "admin/agents", "params": {}, "id": "r1"})
    agents = an.get("result", [])
    print(f"   已注册 {len(agents)} 个 Agent:")
    for a in agents:
        print(f"     {a['agent_id']:15s} {a.get('status','?')}")
    pi_info = [a for a in agents if a.get("agent_id") == "pi"]
    if pi_info:
        pi = pi_info[0]
        print(f"   status={pi.get('status')} display={pi.get('display_name')}")
        assert pi.get("status") == "idle", "pi 未就绪"
    else:
        print("   ERROR: pi 未发现")
        return

    # 2. session/new
    print("\n2. session/new")
    r = await ws_call_simple({
        "method": "invoke",
        "params": {"agent_id": "pi", "method": "session/new", "params": {}},
        "id": "sn1"
    })
    sid = r.get("result", {}).get("sessionId", "")
    if sid:
        print(f"   OK session={sid[:40]}...")
    else:
        print(f"   RESP: {json.dumps(r, indent=2)[:500]}")
        err = r.get("error", {})
        msg = err.get("message", str(r)[:200])
        print(f"   FAILED: {msg}")
        return

    # 3. sessions/list
    print("\n3. sessions/list")
    r = await ws_call_simple({"method": "sessions/list", "params": {"agent_id": "pi"}, "id": "sl1"})
    sessions_res = r.get("result", [])
    print(f"   sessions={len(sessions_res)}")

    # 4. session/prompt (流式)
    print("\n4. session/prompt (流式模式，等待最多120s)...")
    start = time.time()
    r = await ws_call_streaming({
        "method": "invoke",
        "params": {
            "agent_id": "pi",
            "method": "session/prompt",
            "params": {
                "sessionId": sid,
                "prompt": [{"type": "text", "text": "用一句话回复: 1+1=?"}]
            }
        },
        "id": "sp1_stream"
    }, timeout=TIMEOUT)
    dur = time.time() - start
    
    if "error" in r:
        print(f"   FAILED: {r['error'].get('message', 'unknown')}")
    else:
        thought = r.get("thought", "")
        response = r.get("response", "")
        print(f"   OK 耗时={dur:.1f}s")
        if thought:
            print(f"   [思考] {thought[:200]}...")
        if response:
            print(f"   [回复] {response}")

    print("\n" + "=" * 60)
    print("测试完成")

asyncio.run(main())
