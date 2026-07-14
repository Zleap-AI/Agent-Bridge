#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
check_env.py — Agent-Bridge 跨平台环境诊断工具
================================================
检测当前系统是否满足 zleap-bridge-go 各 Agent 的运行前置条件。
支持 Windows / macOS / Linux。

用法:
    py -3.12 check_env.py          # Windows
    python3 check_env.py           # macOS / Linux
    python3 check_env.py --json    # JSON 格式输出（供 CI 或日志分析）

Lzm 2026-07-14
"""

import os
import sys
import shutil
import json
import platform
import subprocess
from pathlib import Path

# ─── Agent 定义（与 registry.go 保持一致） ─────────────────────────────────

AGENTS = [
    {
        "id": "claude-code",
        "display": "Claude Code",
        "cmds": ["claude-agent-acp", "claude"],
        "check_acp": True,
        "acp_cmd": ["claude-agent-acp"],
        "config_dirs": ["~/.claude"],
        "env_keys": ["ANTHROPIC_API_KEY"],
    },
    {
        "id": "opencode",
        "display": "OpenCode",
        "cmds": ["opencode"],
        "check_acp": True,
        "acp_cmd": ["opencode", "acp"],
        "config_dirs": ["~/.config/opencode", "~/.local/share/opencode"],
        "env_keys": ["OPENAI_API_KEY"],
    },
    {
        "id": "codex",
        "display": "Codex",
        "cmds": ["codex-acp", "codex"],
        "check_acp": True,
        "acp_cmd": ["codex-acp"],
        "config_dirs": ["~/.codex"],
        "env_keys": ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"],
    },
    {
        "id": "hermes",
        "display": "Hermes",
        "cmds": ["hermes"],
        "check_acp": True,
        "acp_cmd": ["hermes", "acp"],
        "config_dirs": ["~/.hermes", "~/.local/share/hermes"],
        "env_keys": ["ANTHROPIC_API_KEY"],
    },
    {
        "id": "kimi",
        "display": "Kimi",
        "cmds": ["kimi"],
        "check_acp": True,
        "acp_cmd": ["kimi", "acp"],
        "config_dirs": ["~/.kimi-code"],
        "env_keys": ["MOONSHOT_API_KEY"],
    },
    {
        "id": "gemini",
        "display": "Gemini",
        "cmds": ["gemini"],
        "check_acp": True,
        "acp_cmd": ["gemini", "--experimental-acp"],
        "config_dirs": ["~/.gemini"],
        "env_keys": ["GEMINI_API_KEY"],
    },
    {
        "id": "copilot",
        "display": "GitHub Copilot",
        "cmds": ["copilot"],
        "check_acp": True,
        "acp_cmd": ["copilot", "--acp"],
        "config_dirs": ["~/.copilot"],
        "env_keys": [],
        "note": "使用 GitHub 登录认证，无需 API Key",
    },
    {
        "id": "pi",
        "display": "Pi",
        "cmds": ["pi-acp", "pi"],
        "check_acp": True,
        "acp_cmd": ["pi-acp"],
        "config_dirs": ["~/.pi"],
        "env_keys": [],
        "note": "pi-acp 是 ACP 适配器，内部启动 pi --mode rpc",
    },
    {
        "id": "cursor",
        "display": "Cursor",
        "cmds": ["agent"],
        "check_acp": True,
        "acp_cmd": ["agent", "acp"],
        "config_dirs": ["~/.cursor"],
        "env_keys": ["CURSOR_API_KEY"],
    },
    {
        "id": "glm",
        "display": "GLM",
        "cmds": ["glm-acp-agent"],
        "check_acp": True,
        "acp_cmd": ["glm-acp-agent"],
        "config_dirs": ["~/.local/state/glm-acp-agent"],
        "env_keys": ["Z_AI_API_KEY"],
    },
    {
        "id": "openclaw",
        "display": "OpenClaw",
        "cmds": ["openclaw"],
        "check_acp": True,
        "acp_cmd": ["openclaw", "acp"],
        "config_dirs": ["~/.openclaw"],
        "env_keys": [],
        "note": "需要 OpenClaw Gateway 运行中且模型鉴权有效",
    },
]

# ─── 运行时环境检查 ──────────────────────────────────────────────────────

RUNTIME_CHECKS = [
    ("Node.js", "node", ["--version"]),
    ("npm", "npm", ["--version"]),
    ("Python 3", "python3" if sys.platform != "win32" else "python", ["--version"]),
    ("Go", "go", ["version"]),
    ("Git", "git", ["--version"]),
]


def log(msg: str):
    """输出进度信息"""
    print(f"  ▶ {msg}", flush=True)


def find_cmd(cmd_name: str) -> str | None:
    """在 PATH 中查找命令"""
    return shutil.which(cmd_name)


def run_cmd(cmd: list[str], timeout: int = 8) -> str | None:
    """运行命令并返回 stdout（强制终止，防止进程滞留）"""
    try:
        proc = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if sys.platform == "win32" else 0,
        )
        try:
            stdout, stderr = proc.communicate(timeout=timeout)
            return stdout.strip() or stderr.strip() or None
        except subprocess.TimeoutExpired:
            # 强制终止进程树
            if sys.platform == "win32":
                subprocess.run(["taskkill", "/F", "/T", "/PID", str(proc.pid)],
                               capture_output=True, timeout=5)
            else:
                import signal
                os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
            proc.kill()
            proc.wait(timeout=3)
            return None
    except (FileNotFoundError, OSError):
        return None


def check_acp_startup(cmd_list: list[str]) -> tuple[bool, str]:
    """测试 ACP 子命令是否能正常响应
    对 .cmd/.bat 文件跳过 --help 检测（Windows .cmd 文件可能挂死）
    """
    if sys.platform == "win32":
        exe_path = find_cmd(cmd_list[0]) or ""
        if exe_path.lower().endswith((".cmd", ".bat")):
            return True, "命令找到（跳过 .CMD 的 --help 检测）"

    for flag in ["--help", "-h"]:
        out = run_cmd(cmd_list + [flag], timeout=5)
        if out is not None:
            return True, "命令响应正常"
    return False, "命令无响应（可能缺少 ACP 支持）"


def check_env_key(name: str) -> tuple[bool, str]:
    """检查环境变量是否存在（不泄漏值）"""
    val = os.environ.get(name)
    if val:
        masked = val[:4] + "****" if len(val) > 4 else "****"
        return True, masked
    return False, "未设置"


def expand_home(path: str) -> str:
    """展开 ~ 为用户 home 目录"""
    return str(Path(path).expanduser().resolve())


# ─── 核心检测逻辑 ────────────────────────────────────────────────────────

def diagnose_os() -> dict:
    """诊断操作系统信息"""
    system = platform.system()
    return {
        "os": system,
        "os_version": platform.version(),
        "release": platform.release(),
        "arch": platform.machine(),
        "python": sys.version.split()[0],
        "is_windows": system == "Windows",
        "is_macos": system == "Darwin",
        "is_linux": system == "Linux",
    }


def diagnose_runtime() -> list[dict]:
    """诊断运行时环境"""
    results = []
    for name, cmd, args in RUNTIME_CHECKS:
        exe = find_cmd(cmd)
        if exe:
            ver = run_cmd([exe] + args)
            log(f"运行时 {cmd} → {'发现' if exe else '未发现'} 版本={ver or '?'}")
            results.append({
                "name": name,
                "command": cmd,
                "found": True,
                "path": exe,
                "version": ver or "unknown",
            })
        else:
            log(f"运行时 {cmd} → 未安装")
            results.append({
                "name": name,
                "command": cmd,
                "found": False,
                "path": None,
                "version": None,
            })
    return results


def diagnose_agents() -> list[dict]:
    """诊断各 Agent 安装状态"""
    log(f"正在检查 {len(AGENTS)} 个 Agent ...")
    results = []
    for agent in AGENTS:
        aid = agent["id"]
        display = agent["display"]
        log(f"Agent {aid} ({display}) ...")

        # 1. 检查命令
        found_cmds = {}
        primary_cmd = None
        for cmd in agent["cmds"]:
            exe = find_cmd(cmd)
            if exe:
                found_cmds[cmd] = exe
                if primary_cmd is None:
                    primary_cmd = cmd

        # 2. 检查 ACP
        acp_ok = False
        acp_detail = "N/A"
        if primary_cmd and agent["check_acp"]:
            acp_cmd = [found_cmds.get(agent["acp_cmd"][0], primary_cmd)]
            if len(agent["acp_cmd"]) > 1:
                acp_cmd.append(agent["acp_cmd"][1])
            acp_ok, acp_detail = check_acp_startup(acp_cmd)

        # 3. 检查配置目录
        config_dirs = []
        for d in agent["config_dirs"]:
            expanded = expand_home(d)
            config_dirs.append({
                "path": d,
                "expanded": expanded,
                "exists": os.path.isdir(expanded),
            })

        # 4. 检查环境变量
        env_keys = []
        for k in agent["env_keys"]:
            ok, val = check_env_key(k)
            env_keys.append({"key": k, "set": ok, "value": val if ok else None})

        status = "✅" if found_cmds else "❌"
        if found_cmds and not acp_ok:
            status = "⚠️"

        results.append({
            "id": aid,
            "display": display,
            "status": status,
            "commands": found_cmds,
            "acp_available": acp_ok,
            "acp_detail": acp_detail,
            "config_dirs": config_dirs,
            "env_keys": env_keys,
            "note": agent.get("note"),
        })

    return results


def diagnose_path() -> dict:
    """诊断 PATH 环境变量"""
    log("扫描 PATH 环境变量 ...")
    path_str = os.environ.get("PATH", "")
    sep = ";" if sys.platform == "win32" else ":"
    paths = [p for p in path_str.split(sep) if p]
    return {
        "separator": sep,
        "count": len(paths),
        "paths": paths,
        "contains_node_modules": any("node_modules" in p for p in paths),
        "contains_npm_global": any(
            "npm" in p.lower() or "volta" in p.lower() for p in paths
        ),
    }


def diagnose_npm_global() -> list[dict]:
    """诊断 npm 全局安装包（与 Agent 相关）"""
    npm = find_cmd("npm")
    if not npm:
        return []

    log("检查 npm 全局安装的 Agent 包 ...")
    # npm list -g --depth=0 --json
    out = run_cmd([npm, "list", "-g", "--depth=0", "--json"], timeout=10)
    if not out:
        return []

    try:
        data = json.loads(out)
        deps = data.get("dependencies", {})
        packages = []
        relevant = [
            "pi-acp", "@earendil-works/pi-coding-agent",
            "openclaw",
            "@github/copilot-cli",
            "@agentclientprotocol/codex-acp",
            "glm-acp-agent",
        ]
        for name, info in deps.items():
            if name in relevant:
                packages.append({
                    "name": name,
                    "version": info.get("version", "unknown"),
                })
        return packages
    except (json.JSONDecodeError, KeyError):
        return []


# ─── 输出 ─────────────────────────────────────────────────────────────────

def print_header(title: str):
    width = 60
    print()
    print("=" * width)
    print(f"  {title}")
    print("=" * width)


def print_section(title: str):
    print(f"\n── {title} ─{'─' * max(0, 50 - len(title))}")


def format_found(found: bool) -> str:
    return "✅ 已安装" if found else "❌ 未安装"


def main():
    json_mode = "--json" in sys.argv

    # ─── 执行诊断 ────────────────────────────────────────────────────────
    os_info = diagnose_os()
    runtime = diagnose_runtime()
    agents = diagnose_agents()
    path_info = diagnose_path()
    npm_pkgs = diagnose_npm_global()

    # ─── JSON 输出（用于分析） ──────────────────────────────────────────
    if json_mode:
        report = {
            "timestamp": __import__("datetime").datetime.now().isoformat(),
            "os": os_info,
            "runtime": runtime,
            "agents": agents,
            "path": path_info,
            "npm_global_agents": npm_pkgs,
        }
        print(json.dumps(report, indent=2, ensure_ascii=False))
        return

    # ─── 可读输出（用于人工排查） ──────────────────────────────────────
    print_header(f"Agent-Bridge 环境诊断工具")
    print(f"  时间: {__import__('datetime').datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"  系统: {os_info['os']} {os_info['release']} ({os_info['arch']})")
    print(f"  Python: {os_info['python']}")
    print(f"  平台: {'Windows' if os_info['is_windows'] else 'macOS' if os_info['is_macos'] else 'Linux'}")

    # ─── 运行时环境 ─────────────────────────────────────────────────────
    print_section("运行时环境")
    for r in runtime:
        if r["found"]:
            print(f"  ✅ {r['name']:12s}  {r['version'] or '':20s}  {r['path']}")
        else:
            print(f"  ❌ {r['name']:12s}  未安装")

    # ─── PATH ──────────────────────────────────────────────────────────
    print_section("PATH")
    print(f"  目录数: {path_info['count']}")
    for p in path_info["paths"]:
        marker = ""
        if "node_modules" in p:
            marker = "  ← contains node_modules"
        elif "npm" in p.lower() or "volta" in p.lower():
            marker = "  ← npm/volta"
        print(f"    {p}{marker}")

    # ─── npm 全局 Agent 包 ─────────────────────────────────────────────
    if npm_pkgs:
        print_section("npm 全局安装的 Agent 包")
        for pkg in npm_pkgs:
            print(f"  ✅ {pkg['name']:40s}  v{pkg['version']}")

    # ─── Agent 状态 ────────────────────────────────────────────────────
    print_section("Agent 安装状态")
    print(f"  {'状态':6s}  {'Agent':16s}  {'ACP':6s}  {'命令路径'}")
    print(f"  {'─'*6}  {'─'*16}  {'─'*6}  {'─'*50}")

    for a in agents:
        status_icon = a["status"]
        acp_icon = "✅" if a["acp_available"] else ("⚠️" if a["commands"] else "—")
        cmd_str = ", ".join(a["commands"].values()) if a["commands"] else "—"
        print(f"  {status_icon:6s}  {a['id']:16s}  {acp_icon:6s}  {cmd_str}")
        if a.get("note"):
            print(f"         {'':16s}  {'':6s}  📌 {a['note']}")

    # ─── Agent 详情 ─────────────────────────────────────────────────────
    print_section("Agent 配置目录 & 环境变量")
    for a in agents:
        print(f"\n  [{a['status']} {a['display']} ({a['id']})]")

        # 配置目录
        dirs_found = [d for d in a["config_dirs"] if d["exists"]]
        dirs_missing = [d for d in a["config_dirs"] if not d["exists"]]
        if dirs_found:
            for d in dirs_found:
                print(f"    📁 {d['expanded']}")
        if dirs_missing:
            for d in dirs_missing:
                print(f"    ❌ {d['path']}  (不存在)")

        # 环境变量
        keys_found = [k for k in a["env_keys"] if k["set"]]
        keys_missing = [k for k in a["env_keys"] if not k["set"]]
        if keys_found:
            for k in keys_found:
                print(f"    🔑 {k['key']} = {k['value']}")
        if keys_missing:
            for k in keys_missing:
                print(f"    ⚠️  {k['key']}  未设置")

        # ACP 详情
        if a["acp_detail"] != "N/A":
            print(f"    🔌 ACP: {a['acp_detail']}")

    print_header("诊断完成")
    print(f"Agent 状态汇总:")
    ok = sum(1 for a in agents if a["status"] == "✅")
    warn = sum(1 for a in agents if a["status"] == "⚠️")
    fail = sum(1 for a in agents if a["status"] == "❌")
    print(f"  ✅ {ok} 个正常可用")
    print(f"  ⚠️ {warn} 个命令存在但 ACP 可能有问题")
    print(f"  ❌ {fail} 个未安装")
    print(f"\n发现问题？请检查上面 ❌ / ⚠️ 标记的项。")


if __name__ == "__main__":
    main()
