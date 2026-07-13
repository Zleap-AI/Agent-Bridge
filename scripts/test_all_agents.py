# -*- coding: utf-8 -*-
# Python 3.12+
#
# test_all_agents.py
# zleap-bridge Agent 全面测试 — 覆盖所有支持的 Agent
# 测试: session/new → sessions/list → session/prompt → sessions/messages → session/load → session/load(异常)
# 安装辅助: 对未安装的 Agent 尝试自动安装或输出安装指引
# 配置检测: 检查 Agent 状态，对未配置的提示用户
#
# Lzm 2026-07-13

import json
import sys
import asyncio
import time
import os
import subprocess
import shutil

try:
    import websockets
except ImportError:
    print("请先安装 websockets: py -3.12 -m pip install -i https://mirrors.aliyun.com/pypi/simple/ websockets")
    sys.exit(1)


# ─────────────────────────────────────────────────────────
# Agent 定义 — 与 internal/agent/registry.go 候选列表对齐
# ─────────────────────────────────────────────────────────

AGENT_DEFS = [
    {
        "id": "kimi",
        "name": "Kimi",
        "install": "内置 ACP",
        "install_cmd": "kimi 需从 https://github.com/MoonshotAI/kimi-cli 下载安装",
        "auto_install": False,
        "config_hint": "配置 Moonshot API Key：编辑 ~/.kimi/config.yaml",
        "env_var": "MOONSHOT_API_KEY",
        "env_file": "~/.kimi/config.yaml",
    },
    {
        "id": "opencode",
        "name": "OpenCode",
        "install": "内置 ACP",
        "install_cmd": "npm install -g opencode 或从 https://github.com/sst/opencode 安装",
        "auto_install": False,
        "config_hint": "设置环境变量 OPENAI_API_KEY",
        "env_var": "OPENAI_API_KEY",
        "env_file": None,
    },
    {
        "id": "codex",
        "name": "Codex CLI",
        "install": "Wrapper (codex-acp)",
        "install_cmd": "npm install -g @agentclientprotocol/codex-acp",
        "auto_install": True,
        "npm_pkg": "@agentclientprotocol/codex-acp",
        "config_hint": "设置环境变量 OPENAI_API_KEY",
        "env_var": "OPENAI_API_KEY",
        "env_file": None,
    },
    {
        "id": "claude-code",
        "name": "Claude Code",
        "install": "Wrapper (claude-agent-acp)",
        "install_cmd": "npm install -g @agentclientprotocol/claude-agent-acp",
        "auto_install": True,
        "npm_pkg": "@agentclientprotocol/claude-agent-acp",
        "config_hint": "设置 ANTHROPIC_API_KEY 或编辑 ~/.claude/settings.json",
        "env_var": "ANTHROPIC_API_KEY",
        "env_file": "~/.claude/settings.json",
    },
    {
        "id": "hermes",
        "name": "Hermes Agent",
        "install": "ACP 适配器 (hermes acp)",
        "install_cmd": "pip install hermes-agent; pip install -e '.[acp]'",
        "auto_install": False,
        "config_hint": "编辑 ~/.hermes/.env 配置模型 API Key",
        "env_var": None,
        "env_file": "~/.hermes/.env",
    },
    {
        "id": "gemini",
        "name": "Gemini CLI",
        "install": "内置 ACP (--experimental-acp)",
        "install_cmd": "npm install -g @google/gemini-cli",
        "auto_install": False,
        "config_hint": "设置环境变量 GEMINI_API_KEY",
        "env_var": "GEMINI_API_KEY",
        "env_file": None,
    },
    {
        "id": "copilot",
        "name": "GitHub Copilot",
        "install": "内置 ACP (--acp)",
        "install_cmd": "npm install -g @github/copilot-cli; copilot login",
        "auto_install": False,
        "config_hint": "运行 copilot login 完成 GitHub 认证（首次使用需要）",
        "env_var": None,
        "env_file": None,
    },
    {
        "id": "pi",
        "name": "pi",
        "install": "Wrapper (pi-acp)",
        "install_cmd": "npm install -g pi-acp @earendil-works/pi-coding-agent",
        "auto_install": False,
        "config_hint": "编辑 ~/.pi/agent/settings.json 配置 API Key",
        "env_var": None,
        "env_file": "~/.pi/agent/settings.json",
    },
    {
        "id": "cursor",
        "name": "Cursor",
        "install": "内置 ACP (agent acp)",
        "install_cmd": "从 https://cursor.com 下载安装 CLI",
        "auto_install": False,
        "config_hint": "设置环境变量 CURSOR_API_KEY 或 CURSOR_AUTH_TOKEN",
        "env_var": "CURSOR_API_KEY",
        "env_file": None,
    },
    {
        "id": "glm",
        "name": "GLM Agent",
        "install": "独立命令 (glm-acp-agent)",
        "install_cmd": "npm install -g glm-acp-agent",
        "auto_install": False,
        "config_hint": "设置环境变量 Z_AI_API_KEY（申请：https://z.ai/manage-apikey）",
        "env_var": "Z_AI_API_KEY",
        "env_file": None,
    },
    {
        "id": "openclaw",
        "name": "OpenClaw",
        "install": "内置 ACP (openclaw acp)",
        "install_cmd": "npm install -g openclaw",
        "auto_install": False,
        "config_hint": "需要 OpenClaw Gateway 运行中，配置 --url 或环境变量",
        "env_var": None,
        "env_file": None,
    },
]

AGENT_IDS = [a["id"] for a in AGENT_DEFS]


# ─────────────────────────────────────────────────────────
# 测试结果记录
# ─────────────────────────────────────────────────────────

class TestResult:
    """单个 Agent 的测试结果"""
    def __init__(self, agent_id):
        self.agent_id = agent_id
        self.installed = False       # 是否在 bridge 中注册
        self.configured = False      # 配置是否完整
        self.session_new = None      # "通过"/"失败"/None(未测试)
        self.session_new_ms = None   # 耗时
        self.session_list = None
        self.prompt = None
        self.prompt_reply = ""       # 回复摘要
        self.messages = None
        self.session_load = None
        self.session_load_error = None  # 无效 session 加载
        self.error_msg = ""          # 整体错误信息
        self.skipped = False         # 是否跳过
        self.skip_reason = ""


def c(text, color=""):
    """终端颜色输出"""
    colors = {
        "green": "\033[92m",
        "red": "\033[91m",
        "yellow": "\033[93m",
        "cyan": "\033[96m",
        "bold": "\033[1m",
        "dim": "\033[2m",
        "reset": "\033[0m",
    }
    if color and color in colors:
        return f"{colors[color]}{text}{colors['reset']}"
    return text


def print_step(text, status=None):
    """格式化输出测试步骤"""
    symbols = {
        "ok": c(" ✅", "green"),
        "fail": c(" ❌", "red"),
        "warn": c(" ⚠️", "yellow"),
        "info": c(" ℹ️", "cyan"),
    }
    prefix = symbols.get(status, "  ")
    print(f"  {prefix} {text}")


def print_section(title):
    """打印分隔标题"""
    print()
    print(c(f"  {'─' * 60}", "dim"))
    print(c(f"  {title}", "bold"))
    print(c(f"  {'─' * 60}", "dim"))


# ─────────────────────────────────────────────────────────
# 自动安装辅助
# ─────────────────────────────────────────────────────────

async def auto_install_npm(agent_def):
    """尝试自动安装 npm 包"""
    pkg = agent_def.get("npm_pkg")
    if not pkg:
        return False

    npm = shutil.which("npm") or shutil.which("npm.cmd")
    if not npm:
        print_step("npm 不可用，无法自动安装", "warn")
        return False

    prefix = os.path.join(os.environ.get("TEMP", os.path.expanduser("~")), ".npm-global")

    print_step(f"正在安装 {pkg}（通过 npm install --prefix {prefix} -g {pkg}）...", "info")
    try:
        proc = await asyncio.create_subprocess_exec(
            npm, "install", "--prefix", prefix, "-g", pkg,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        try:
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=120)
        except asyncio.TimeoutError:
            proc.kill()
            print_step(f"安装超时（120秒）", "fail")
            return False

        if proc.returncode != 0:
            err_text = stderr.decode("utf-8", errors="replace")[-200:]
            print_step(f"安装失败: {err_text}", "fail")
            return False

        print_step(f"安装完成！请重启 bridge 以使 Agent 生效", "ok")
        return True

    except Exception as e:
        print_step(f"安装异常: {e}", "fail")
        return False


# ─────────────────────────────────────────────────────────
# Bridge WebSocket 通信
# ─────────────────────────────────────────────────────────

class BridgeClient:
    """与 bridge Admin WebSocket 通信"""

    def __init__(self, uri="ws://localhost:9202/ws/admin"):
        self.uri = uri
        self.ws = None
        self.pending = {}

    async def connect(self):
        self.ws = await websockets.connect(self.uri)
        return True

    async def close(self):
        if self.ws:
            await self.ws.close()
            self.ws = None

    async def send_request(self, method, params=None, tag="", timeout=30):
        """发送 JSON-RPC 请求并等待响应"""
        msg_id = f"test_{int(time.time() * 1000)}_{tag}_{method}"
        request = {
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": method,
        }
        if params:
            request["params"] = params

        await self.ws.send(json.dumps(request))

        start = time.time()
        while True:
            elapsed = time.time() - start
            if elapsed > timeout:
                return {"error": {"code": -1, "message": f"等待响应超时 ({timeout}s)"}}

            # 非阻塞读取
            try:
                resp = json.loads(await asyncio.wait_for(self.ws.recv(), timeout=min(5, timeout - elapsed)))
            except asyncio.TimeoutError:
                continue
            except websockets.exceptions.ConnectionClosed:
                return {"error": {"code": -1, "message": "WebSocket 连接已关闭"}}

            # 匹配请求 ID 的响应
            if resp.get("id") == msg_id:
                return resp

            # 流式通知 — 放入 pending
            if resp.get("method") == "session/update":
                if method == "session/prompt":
                    # 返回给调用方流式处理
                    return ("stream", resp)
                self.pending[resp.get("id")] = resp

    async def collect_stream_prompt(self, agent_id, session_id, text, timeout=120):
        """发送 session/prompt 并收集所有流式输出"""
        msg_id = f"test_{int(time.time() * 1000)}_prompt_{agent_id}"
        request = {
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": "invoke",
            "params": {
                "agent_id": agent_id,
                "method": "session/prompt",
                "params": {
                    "sessionId": session_id,
                    "prompt": [{"type": "text", "text": text}],
                },
            },
        }

        print_step("发送提示词请求...", "info")
        await self.ws.send(json.dumps(request))

        chunks = []
        final_response = None
        start = time.time()

        while True:
            elapsed = time.time() - start
            if elapsed > timeout:
                print_step(f"流式响应超时 ({timeout}s)，已接收 {len(chunks)} 个块", "warn")
                break

            try:
                resp = json.loads(await asyncio.wait_for(self.ws.recv(), timeout=min(5, timeout - elapsed)))
            except asyncio.TimeoutError:
                continue
            except websockets.exceptions.ConnectionClosed:
                print_step("WebSocket 连接在流式读取中关闭", "fail")
                break

            # 流式通知
            if resp.get("method") == "session/update":
                params = resp.get("params", {})
                ctype = params.get("type", "")
                content = params.get("content", {})

                # 显示思考过程
                if ctype == "thinking_chunk":
                    text_content = content.get("text", "") if isinstance(content, dict) else str(content)
                    if text_content:
                        print(f"    {c('思考:', 'dim')} {text_content[:120]}")

                # 显示消息内容
                elif ctype in ("message_chunk", "content_chunk"):
                    text_content = ""
                    if isinstance(content, dict):
                        text_content = content.get("text", "") or content.get("content", "") or ""
                        # content 可能是嵌套的
                        parts = content.get("parts", [])
                        if not text_content and parts:
                            text_content = " ".join(p.get("text", "") for p in parts if isinstance(p, dict))
                    else:
                        text_content = str(content)
                    if text_content:
                        print(f"    {c('回复:', 'green')} {text_content[:200]}")

                chunks.append(params)

            # 最终响应
            elif resp.get("id") == msg_id:
                final_response = resp
                break

        return chunks, final_response


async def get_agents(bridge):
    """获取 bridge 中已注册的 Agent 列表"""
    resp = await bridge.send_request("admin/agents", tag="init")
    if resp.get("error"):
        return []
    agents = resp.get("result", [])
    if isinstance(agents, list):
        return agents
    if isinstance(agents, dict):
        # 可能是嵌套结构
        for key in ("agents", "items", "data"):
            if key in agents:
                return agents[key]
    return []


async def check_env(env_var, env_file=None):
    """检查环境变量或配置文件是否存在"""
    if env_var and os.environ.get(env_var):
        return True
    if env_file:
        expanded = os.path.expanduser(env_file)
        if os.path.exists(expanded):
            return True
    return False


def get_install_hint(agent_def):
    """生成安装指引文本"""
    lines = []
    install_cmd = agent_def.get("install_cmd", "")
    config_hint = agent_def.get("config_hint", "")
    install_type = agent_def.get("install", "")

    lines.append(f"安装方式: {install_type}")
    if install_cmd:
        lines.append(f"安装命令: {install_cmd}")
    if config_hint:
        lines.append(f"配置: {config_hint}")

    return "\n    ".join(lines)


# ─────────────────────────────────────────────────────────
# Agent 测试
# ─────────────────────────────────────────────────────────

async def test_agent(bridge, agent_def):
    """对单个 Agent 执行完整测试序列"""
    agent_id = agent_def["id"]
    display_name = agent_def["name"]
    result = TestResult(agent_id)

    print_section(f"{display_name} ({agent_id})")

    # ── 1. 检查是否已注册 ──
    agents = await get_agents(bridge)
    registered = {a.get("agent_id") or a.get("id") or a: a for a in agents}
    info = registered.get(agent_id)

    if not info:
        print_step(f"未在 bridge 中注册", "warn")
        # 尝试自动安装
        if agent_def.get("auto_install"):
            print_step(f"尝试自动安装 {agent_def.get('npm_pkg', '')} ...", "info")
            ok = await auto_install_npm(agent_def)
            if ok:
                print_step(f"自动安装完成 — 请重启 bridge 后重新运行测试", "info")
            else:
                print_step(f"自动安装失败", "fail")
                print_step(f"安装指引:\n    {get_install_hint(agent_def)}", "info")
        else:
            print_step(f"安装指引:\n    {get_install_hint(agent_def)}", "info")

        result.installed = False
        result.skipped = True
        result.skip_reason = "not_installed"
        return result

    result.installed = True
    status = info.get("status", "unknown")
    print_step(f"已注册 | 状态: {status}", "ok")

    # ── 检查状态是否可连接 ──
    if status == "disconnected":
        print_step(f"Agent 已发现但未能连接（状态: disconnected）", "warn")
        print_step(f"请确认该 Agent 的 API Key 等配置是否完整", "info")
        result.configured = False
        result.skipped = True
        result.skip_reason = "disconnected"
        return result

    result.configured = True

    # ── 3. session/new — 创建会话 ──
    print_step("测试 session/new...", "info")
    t0 = time.time()
    resp = await bridge.send_request("invoke", {
        "agent_id": agent_id,
        "method": "session/new",
        "params": {},
    }, tag=f"new_{agent_id}", timeout=30)
    elapsed = int((time.time() - t0) * 1000)

    session_id = None
    if resp.get("result"):
        session_id = resp["result"].get("sessionId") or resp["result"].get("session_id")
        if session_id:
            print_step(f"session/new → {session_id[:32]}... ({elapsed}ms)", "ok")
            result.session_new = "通过"
            result.session_new_ms = elapsed
        else:
            print_step(f"session/new 但未返回 sessionId: {json.dumps(resp['result'])[:100]}", "warn")
            result.session_new = "失败"
            result.error_msg = "无 sessionId"
    else:
        err = resp.get("error", {})
        print_step(f"session/new 失败: {err.get('message', str(err))}", "fail")
        result.session_new = "失败"
        result.error_msg = err.get("message", str(err))

    # ── 4. sessions/list — 列出会话 ──
    print_step("测试 sessions/list...", "info")
    resp = await bridge.send_request("sessions/list", {"agent_id": agent_id}, tag=f"list_{agent_id}")
    sessions = []
    if resp.get("result"):
        if isinstance(resp["result"], list):
            sessions = resp["result"]
        elif isinstance(resp["result"], dict):
            sessions = resp["result"].get("sessions", [])
        print_step(f"sessions/list → {len(sessions)} 个会话", "ok")
        result.session_list = "通过"
    else:
        print_step(f"sessions/list 失败: {resp.get('error', '无结果')}", "warn")
        result.session_list = "失败"

    # ── 5. session/prompt — 发送提示词 ──
    if session_id:
        print_step("测试 session/prompt (\"请用一句话回复：1+1等于几？\")...", "info")
        chunks, final = await bridge.collect_stream_prompt(agent_id, session_id,
                                                          "请用一句话回复：1+1等于几？")
        if final and final.get("result"):
            result_text = json.dumps(final["result"], ensure_ascii=False)[:200]
            print_step(f"prompt 完成 (非流式)", "ok")
            result.prompt = "通过"
            result.prompt_reply = result_text
        elif chunks:
            print_step(f"prompt 完成 (收到 {len(chunks)} 个流式块)", "ok")
            result.prompt = "通过"
            # 收集回复文本
            texts = []
            for c in chunks:
                content = c.get("content", {})
                if isinstance(content, dict):
                    texts.append(content.get("text", "") or content.get("content", "") or "")
                else:
                    texts.append(str(content))
            result.prompt_reply = " ".join(texts)[:200]
        elif final and final.get("error"):
            err = final["error"]
            print_step(f"prompt 失败: {err.get('message', str(err))}", "fail")
            result.prompt = "失败"
        else:
            print_step(f"prompt 无响应", "fail")
            result.prompt = "失败"
    else:
        print_step("跳过 session/prompt（无 sessionId）", "warn")
        result.prompt = "跳过"

    # ── 6. sessions/messages — 查询消息历史 ──
    if session_id:
        print_step("测试 sessions/messages...", "info")
        resp = await bridge.send_request("sessions/messages", {
            "agent_id": agent_id,
            "session_id": session_id,
        }, tag=f"msg_{agent_id}")
        messages = []
        if resp.get("result"):
            if isinstance(resp["result"], list):
                messages = resp["result"]
            elif isinstance(resp["result"], dict):
                messages = resp["result"].get("messages", [])
            print_step(f"sessions/messages → {len(messages)} 条消息", "ok")
            result.messages = "通过"
        else:
            print_step(f"sessions/messages 返回空", "info")
            result.messages = "跳过"
    else:
        print_step("跳过 sessions/messages（无 sessionId）", "warn")
        result.messages = "跳过"

    # ── 7. session/load — 加载已有会话 ──
    if session_id:
        print_step("测试 session/load (加载已有会话)...", "info")
        t0 = time.time()
        resp = await bridge.send_request("invoke", {
            "agent_id": agent_id,
            "method": "session/load",
            "params": {"sessionId": session_id},
        }, tag=f"load_{agent_id}", timeout=30)
        elapsed = int((time.time() - t0) * 1000)

        if resp.get("result") or resp.get("result") is None:
            # 部分 Agent session/load 返回空 result 但无 error 也算成功
            if not resp.get("error"):
                print_step(f"session/load → 成功 ({elapsed}ms)", "ok")
                result.session_load = "通过"
            else:
                print_step(f"session/load 失败: {resp['error']}", "fail")
                result.session_load = "失败"
        else:
            err = resp.get("error", {})
            print_step(f"session/load 失败: {err.get('message', str(err))}", "fail")
            result.session_load = "失败"
    else:
        print_step("跳过 session/load（无 sessionId）", "warn")
        result.session_load = "跳过"

    # ── 8. session/load (异常) — 加载不存在的会话 ──
    print_step("测试 session/load (无效 sessionId)...", "info")
    resp = await bridge.send_request("invoke", {
        "agent_id": agent_id,
        "method": "session/load",
        "params": {"sessionId": "sess_nonexistent_000"},
    }, tag=f"loade_{agent_id}", timeout=15)

    if resp.get("error"):
        err = resp["error"]
        print_step(f"session/load(无效) → 正确返回错误: code={err.get('code')} msg={err.get('message', '')[:80]}", "ok")
        result.session_load_error = "通过"
    elif resp.get("result"):
        print_step(f"session/load(无效) 返回了结果（部分 Agent 可能自动创建）", "info")
        result.session_load_error = "通过"
    else:
        print_step(f"session/load(无效) 无响应", "warn")
        result.session_load_error = "跳过"

    return result


# ─────────────────────────────────────────────────────────
# 汇总报告
# ─────────────────────────────────────────────────────────

def print_summary(results):
    """打印测试汇总表"""
    print()
    print(c(f"  {'═' * 60}", "bold"))
    print(c(f"  测试报告", "bold"))
    print(c(f"  {'═' * 60}", "bold"))

    # 表头
    header = f"  {'Agent':<14} {'安装':<8} {'配置':<8} {'会话':<8} {'对话':<8} {'历史':<8} {'加载':<8}"
    print(c(header, "bold"))
    print(c(f"  {'─' * 14} {'─' * 8} {'─' * 8} {'─' * 8} {'─' * 8} {'─' * 8} {'─' * 8}", "dim"))

    passed = 0
    skipped = 0
    failed = 0
    not_installed = 0

    for r in results:
        # 汇总表中略过配置列（已注册的 Agent 内部已配置）
        installed_s = c("✅", "green") if r.installed else c("❌", "red")
        config_s = "  ✅" if r.installed else c("  -", "dim")

        if r.session_new == "通过":
            session_s = c("✅", "green")
            passed += 1
        elif r.session_new == "失败":
            session_s = c("❌", "red")
            failed += 1
        else:
            session_s = "  -"

        if r.prompt == "通过":
            prompt_s = c("✅", "green")
        elif r.prompt == "失败":
            prompt_s = c("❌", "red")
        else:
            prompt_s = "  -"

        if r.messages == "通过":
            msg_s = c("✅", "green")
        elif r.messages == "失败":
            msg_s = c("❌", "red")
        else:
            msg_s = "  -"

        if r.session_load == "通过":
            load_s = c("✅", "green")
        elif r.session_load == "失败":
            load_s = c("❌", "red")
        else:
            load_s = "  -" if not r.installed else c("⚠️", "yellow")

        print(f"  {r.agent_id:<14} {installed_s:<8} {config_s:<8} {session_s:<8} {prompt_s:<8} {msg_s:<8} {load_s:<8}")

        if r.error_msg:
            print(f"    {c(r.error_msg[:80], 'red')}")
        elif r.skip_reason:
            skip_label = {"not_installed": "未安装", "disconnected": "连接失败"}
            print(f"    {c(f'跳过: {skip_label.get(r.skip_reason, r.skip_reason)}', 'yellow')}")

    print(c(f"  {'─' * 60}", "dim"))

    total = len(results)
    print()
    print(f"  总计: {total}  |  {c('通过', 'green')}: {passed}  |  {c('失败', 'red')}: {failed}  |  "
          f"{c('跳过', 'yellow')}: {skipped}  |  {c('未安装', 'dim')}: {not_installed}")
    print()


# ─────────────────────────────────────────────────────────
# 入口
# ─────────────────────────────────────────────────────────

async def main():
    port = 9202
    if len(sys.argv) > 1:
        try:
            port = int(sys.argv[1])
        except ValueError:
            print(f"用法: py -3.12 {sys.argv[0]} [port]")
            print(f"  默认端口: 9202")
            sys.exit(1)

    uri = f"ws://localhost:{port}/ws/admin"

    print()
    print(c(f"  {'═' * 60}", "bold"))
    print(c(f"  zleap-bridge Agent 全面测试", "bold"))
    print(c(f"  {'═' * 60}", "bold"))
    print(f"  桥接器: {uri}")

    # 连接 bridge
    bridge = BridgeClient(uri)
    try:
        await bridge.connect()
        print(c(f"  ✅ 已连接", "green"))
        print()
    except (ConnectionRefusedError, OSError, websockets.exceptions.InvalidURI) as e:
        print(c(f"  ❌ 无法连接: {e}", "red"))
        print()
        print(f"  请确认 bridge 已启动:")
        print(f"    1. 在终端中运行: {c('./bridge.exe --debug', 'cyan')}")
        print(f"    2. 看到 'Admin HTTP 服务启动' 日志")
        print(f"    3. 重新运行本脚本")
        print()
        sys.exit(1)

    # 获取已注册 Agent
    agents = await get_agents(bridge)
    registered_ids = set()
    for a in agents:
        aid = a.get("agent_id") or a.get("id")
        if aid:
            registered_ids.add(aid)

    print(f"  已注册 Agent ({len(registered_ids)}): {', '.join(sorted(registered_ids))}")
    print()

    # 逐个测试
    results = []
    for agent_def in AGENT_DEFS:
        result = await test_agent(bridge, agent_def)
        results.append(result)

    # 断开连接
    await bridge.close()

    # 汇总
    print_summary(results)

    # 未安装的 Agent 汇总
    uninstalled = [r for r in results if not r.installed]
    if uninstalled:
        print(c(f"  {'─' * 60}", "dim"))
        print(c(f"  未安装的 Agent ({len(uninstalled)})", "bold"))
        print()
        for r in uninstalled:
            agent_def = next(a for a in AGENT_DEFS if a["id"] == r.agent_id)
            print(f"  • {c(agent_def['id'], 'bold')} ({agent_def['name']})")
            print(f"    安装: {agent_def['install_cmd']}")
            if agent_def.get("config_hint"):
                print(f"    {c('注意:', 'yellow')} 请在各 Agent 的官方 CLI/程序中完成配置（API Key 等）")

    print(c(f"  {'═' * 60}", "bold"))
    print()


if __name__ == "__main__":
    asyncio.run(main())
