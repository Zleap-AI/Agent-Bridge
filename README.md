<p align="center">
  <img src="docs/logo.svg" alt="Zleap AI" width="220" />
</p>

<h1 align="center">Agent-Bridge</h1>

<p align="center">
  <a href="https://github.com/Zleap-AI/Agent-Bridge/releases/latest"><img alt="Version" src="https://img.shields.io/badge/version-v0.4.0-18181b" /></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25%2B-00ADD8" />
  <img alt="Protocol" src="https://img.shields.io/badge/ACP-v1%20%7C%20JSON--RPC%202.0-2563eb" />
  <img alt="Platforms" src="https://img.shields.io/badge/Local-macOS%20%7C%20Windows%20%7C%20Linux-18181b" />
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-16855b" /></a>
</p>

<p align="center"><strong>让本地 AI Agent 独立运行，也能通过自托管 Server 远程调用</strong></p>
<p align="center">自动发现本机 Agent，统一管理 Session 与 Message；用户电脑无需公网 IP，也不需要开放本地端口。</p>

<p align="center">
  <a href="#项目介绍">项目介绍</a> ·
  <a href="#用户指南">用户指南</a> ·
  <a href="#开发者指南">开发者指南</a>
</p>

---

## 项目介绍

Agent-Bridge 由同一个开源项目中的两个程序组成：

| 程序 | 运行位置 | 用途 |
| --- | --- | --- |
| **Agent-Bridge Local** | 用户电脑 | 发现并调用本机 Agent，提供 Local Console，主动连接 Server |
| **Agent-Bridge Server** | 公网 Linux 服务器 | 接收 Local 主动连接，提供 Remote Console 与 Caller API |

<p align="center">
  <img src="docs/assets/readme/architecture-overview.png" alt="Agent-Bridge 架构" width="960" />
</p>

Local 可以完全脱离 Server 使用。需要远程调用时，Local 主动建立 WS/WSS 长连接，所以用户电脑即使在路由器或 NAT 后面、没有公网 IP，也能被自己的 Server 调用。

### 核心能力

| 能力 | 说明 |
| --- | --- |
| 自动发现 | 扫描本机可执行文件，只显示实际安装的 Agent |
| 统一调用 | 隐藏不同 Agent 的命令、参数和 ACP 差异 |
| Session 管理 | 创建、恢复和切换 Session，历史数据保存在用户电脑 |
| 流式 Message | 将 Agent 输出统一为文本、推理、Session 更新、完成和错误事件 |
| 自托管远程连接 | Local 主动连接用户自己的 Server，不要求本机公网 IP |
| 两套正式 Console | Local Console 测试当前电脑；Remote Console 管理远程 Device |
| Caller API | 通过 REST 与 SSE 把 Agent 能力接入其他产品 |
| 单文件部署 | Console 已嵌入 Go 二进制，运行时不需要 Go、Node.js 或外部数据库 |

### 数据边界

- Agent 账号、模型 API Key、插件和工作目录仍由各 Agent 自己管理。
- Session、Message 正文和 Agent 输出只保存在 Device，不写入 Server 数据库。
- Server 使用 SQLite 保存 Device、凭证和最近 1000 条无正文调用记录。
- Server 离线不会影响 Local Console 和本地调用。

### 支持的 Agent

目前支持以下 11 个 Agent。Local 启动后只显示当前电脑实际安装并检测到的 Agent。

| Agent | 使用前提 | 状态 |
| --- | --- | --- |
| Claude Code | 已安装并能正常使用 Claude Code | ✅ |
| OpenCode | 已安装支持 ACP 的 OpenCode | ✅ |
| Codex | 已安装并能正常使用 Codex | ✅ |
| Hermes | 已安装 Hermes CLI | ✅ |
| Kimi | 已安装 Kimi CLI | ✅ |
| Gemini | 已安装 Gemini CLI | ✅ |
| GitHub Copilot | 已安装 Copilot CLI | ✅ |
| Pi | 已安装 Pi ACP 适配器 | ✅ |
| Cursor | 已安装 Cursor Agent CLI | ✅ |
| GLM | 已安装 GLM ACP 适配器 | ✅ |
| OpenClaw | Gateway 正常运行且模型鉴权有效 | ✅ |

> ✅ 表示 Agent-Bridge 已完成对应 Agent 的连接支持，不代表该 Agent 的账号、模型配置或网络一定可用。

Claude Code 与 Codex 缺少连接适配器时可以自动安装；自动安装要求电脑上已有 Node.js 与 npm。具体检测命令和启动方式放在[开发者指南](#agent-适配器)。

---

## 用户指南

Agent-Bridge Local 是日常入口。只在当前电脑使用 Agent 时，不需要安装 Server；只有需要跨网络访问时，才需要连接一台已经部署好的 Agent-Bridge Server。

### 安装并打开 Local

| 系统 | 推荐方式 |
| --- | --- |
| Windows | 从 [GitHub Releases](https://github.com/Zleap-AI/Agent-Bridge/releases/latest) 下载对应版本并双击运行 |
| macOS / Linux | 按[开发者指南中的一键安装方式](#安装-local)完成安装，安装成功后会自动打开 Local Console |

普通用户不需要安装 Go，也不需要重新构建。Windows 首次运行会为当前用户注册后台自启动；macOS 和 Linux 的一键安装同样会注册当前用户的后台服务。

Local Console 默认地址是 [http://localhost:9202](http://localhost:9202)，只供当前电脑访问，不要通过局域网或公网暴露。

### 本地使用

1. 打开 Local Console，左侧会列出当前电脑实际可用的 Agent。
2. 选择一个 Agent，点击右上角的“新建 Session”。
3. 在底部输入 Message 并发送，即可查看流式回答。
4. 之后可以从顶部切换已有 Session，继续原来的对话。

如果列表中没有某个 Agent，先确认它已经安装并能独立完成一次对话，然后重启 Agent-Bridge Local。

### 远程连接

如果还没有可用的 Server，请登录一台公网 Linux 服务器并运行：

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-server.sh | sudo bash
```

默认不需要输入公网 IP，脚本会自动识别并保存。安装完成后，终端会显示一次性的 Setup URL。用浏览器打开该地址，设置 Owner Password 后即可进入 Remote Console。如果页面打不开，请在云服务器安全组中放行 TCP 9201。只有自动识别失败且链接中出现 `SERVER_PUBLIC_IP` 时，才需要将它替换为服务器的公网 IP。域名、HTTPS 和反向代理配置参见[开发者指南](#部署-server)。

1. 进入 Remote Console，在左侧底部打开“Pairing”。
2. 生成一个 Pairing Code。Code 有效期为 10 分钟，只能使用一次；新 Code 会替换旧 Code。
3. 回到用户电脑的 Local Console，打开“远程连接”。
4. 输入 Server 地址和 Pairing Code，确认连接。
5. Remote Console 出现这台 Device 后，即表示远程连接已经完成。

Local 会主动连接 Server，因此用户电脑不需要公网 IP，也不需要开放本地端口。同一个 Local 一次只连接一个 Server，切换前会要求再次确认。

在 Remote Console 删除 Device 会立即撤销它的远程连接权限，但不会删除电脑上的 Session 和 Message。

### 远程调用

1. 打开 Remote Console，选择一台在线 Device。
2. 选择该 Device 上的 Agent。
3. 创建或切换 Session，然后发送 Message。

Device 离线时，页面会立即提示，不会把请求排队到设备重新上线后执行。公开网络建议使用 HTTPS/WSS；直接使用 IP 和 HTTP 时，Console 会持续显示未加密警告。

### 常见问题

| 现象 | 常见原因 | 处理方式 |
| --- | --- | --- |
| Local Console 没有 Agent | Agent 未安装，或后台服务没有找到它 | 确认 Agent 本身能正常运行，再重启 Local |
| Agent 显示可用但发送失败 | Agent 尚未登录、模型配置无效或网络不可用 | 先直接使用 Agent 完成一次对话 |
| Pairing Code 无效 | Code 已过期、已使用，或被新 Code 替换 | 在 Remote Console 重新生成 |
| Device 显示离线 | Local 未运行、连接权限已撤销或网络不通 | 检查 Local 状态、Server 地址和网络 |
| 能看到 Device 但无法调用 | Agent 不可用，或 Local 正在重新连接 | 查看 Device 上的 Agent 状态和两端日志 |
| Server 页面打不开 | 服务未启动、端口未开放或反向代理配置错误 | 联系服务器管理员检查部署状态 |
| 页面提示连接未加密 | 当前使用 HTTP/WS | 个人测试可以继续；公开网络应配置 HTTPS/WSS |

---

## 开发者指南

以下内容面向部署者和集成开发者，集中说明命令行安装、Server 配置、Caller API、数据路径、源码构建和发布验证。

### Agent 适配器

| Agent | 检测的本地命令 | ACP 启动方式 |
| --- | --- | --- |
| Claude Code | `claude-agent-acp` | `claude-agent-acp` |
| OpenCode | `opencode` | `opencode acp` |
| Codex | `codex-acp` / `codex` | 优先使用 `codex-acp` |
| Hermes | `hermes` | `hermes acp` |
| Kimi | `kimi` | `kimi acp` |
| Gemini | `gemini` | `gemini --experimental-acp` |
| GitHub Copilot | `copilot` | `copilot --acp` |
| Pi | `pi-acp` | `pi-acp` |
| Cursor | `agent` | `agent acp` |
| GLM | `glm-acp-agent` | `glm-acp-agent` |
| OpenClaw | `openclaw` | `openclaw acp` |

Agent 显示 `idle` 只表示 ACP 进程可用。账号登录、模型 API Key、插件和 Agent 自身网络仍由对应 Agent 管理。

> **OpenClaw 特殊说明**：`openclaw acp` 需要连接本地的 OpenClaw Gateway 服务（WebSocket `127.0.0.1:18789`）。使用 OpenClaw 前，请先在终端独立启动 Gateway：
> ```bash
> openclaw gateway run --port 18789
> ```
> 如果 Gateway 未运行，`openclaw acp` 会报 `ECONNREFUSED` 并非 bridge 的代码问题。

### 安装 Local

#### macOS / Linux 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-local.sh | bash
```

脚本会下载匹配当前系统的最新稳定版二进制、校验 SHA-256、注册当前用户的后台服务、等待健康检查通过，再打开 Local Console。重复运行同一命令即可升级；如果新版本启动失败，脚本会恢复原来的二进制和后台服务。

macOS 使用 `launchd`。Linux 快速安装目前要求 `systemd`，并会尝试启用当前用户的 linger，让 Local 在退出登录后继续运行。如果系统拒绝，安装仍会完成并打印恢复命令。容器、WSL 或非 `systemd` 发行版请直接运行二进制。

#### Windows

1. 从 [GitHub Releases](https://github.com/Zleap-AI/Agent-Bridge/releases/latest) 下载 `agent-bridge_v0.4.0_windows_amd64.exe` 或 ARM64 版本。
2. 将文件改名为 `agent-bridge.exe`，双击运行。
3. 打开 [http://localhost:9202](http://localhost:9202)。

运行日志按日期保存在 `%USERPROFILE%\.agent-bridge\logs\`。可以在 PowerShell 中持续查看当天日志：

```powershell
Get-Content "$env:USERPROFILE\.agent-bridge\logs\$(Get-Date -Format yyyy-MM-dd).log" -Wait
```

#### 直接运行二进制

不希望注册后台服务时，可以从 Release 下载原始二进制：

```bash
chmod +x agent-bridge_v0.4.0_darwin_arm64
./agent-bridge_v0.4.0_darwin_arm64
```

### 部署 Server

远程闭环只需要一台公网 Linux 服务器。Server 支持 Linux x86_64 / ARM64、systemd，以及 Ubuntu、Debian、CentOS、RHEL、Rocky Linux、AlmaLinux 和 Fedora。

#### 一键安装

默认安装不需要预先填写公网 IP：

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-server.sh | sudo bash
```

极简系统可能没有 `curl` 或 CA 证书。Ubuntu / Debian 先安装 `ca-certificates` 与 `curl`；RHEL 系发行版使用 `dnf` 安装同名软件包。Server 运行时不需要 Go、Node.js 或 Docker。

安装脚本会下载并校验二进制、创建独立系统用户和数据目录、注册 systemd 服务、等待健康检查通过，并输出一次性的 Setup URL。未配置公网地址时，脚本会先检查服务器网卡，再依次通过 AWS Check IP 与 ipify 查询出口公网 IPv4；只接受合法公网地址，并将结果保存到 Server 配置。默认监听 `0.0.0.0:9201`。

脚本不会修改防火墙或云安全组。请自行允许 TCP `9201`，或通过反向代理提供 HTTPS/WSS。如果脚本无法确认公网地址，输出链接中的 `SERVER_PUBLIC_IP` 需要手动替换；也可以重新安装时通过 `AGENT_BRIDGE_PUBLIC_URL` 显式指定公网地址。

#### 使用 Nginx 提供 HTTPS/WSS

公开网络推荐只让 Nginx 访问本机 `9201`。先重跑安装脚本，把 Server 改为仅监听回环地址；同时关闭公网 TCP `9201`，只保留 HTTPS 端口：

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-server.sh | sudo env AGENT_BRIDGE_LISTEN_ADDR=127.0.0.1:9201 AGENT_BRIDGE_PUBLIC_URL=https://bridge.example.com bash
```

下面配置同时支持 Remote Console、Device WebSocket 和 Caller API 的 SSE 流。`proxy_buffering off` 不能省略，否则部分代理会缓存流式输出。

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name bridge.example.com;

    ssl_certificate     /etc/letsencrypt/live/bridge.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/bridge.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:9201;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

配置后，将安装参数和 Local Console 中的 Server 地址都改为 `https://bridge.example.com`。Nginx 会把 Device 的 HTTPS 地址升级为 WSS，无需另外开放 WebSocket 端口。

#### Owner Password 与诊断

打开安装脚本输出的 Setup URL，设置 Owner Password。第一版没有用户名或账号体系，之后直接使用该密码进入 Remote Console。

Setup URL 只在首次设置完成前有效。链接丢失时，可以在服务器生成一个新链接，旧链接会立即失效：

```bash
sudo -u agent-bridge env AGENT_BRIDGE_PUBLIC_URL=http://PUBLIC_IP:9201 \
  agent-bridge-server setup-url
```

忘记密码时只能在服务器本机重置：

```bash
sudo -u agent-bridge agent-bridge-server reset-password
```

Owner 登录状态最长保持 30 天；修改或重置密码会使已有登录状态全部失效。常用诊断命令：

```bash
sudo systemctl status agent-bridge-server
sudo journalctl -u agent-bridge-server -f
```

### Caller API

开发者可以把 Device、Agent、Session 和 Message 能力接入自己的产品。先在 Remote Console 的“API Key”页面创建 Key；明文只显示一次，请立即妥善保存。

#### 最短调用流程

```bash
export AGENT_BRIDGE_SERVER="http://your-server:9201"
export AGENT_BRIDGE_API_KEY="abk_your_key"
```

查询在线 Device：

```bash
curl -sS "$AGENT_BRIDGE_SERVER/api/v1/devices" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"
```

创建全新 Session：

```bash
curl -sS -X POST \
  "$AGENT_BRIDGE_SERVER/api/v1/devices/DEVICE_ID/agents/AGENT_ID/sessions" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"
```

发送 Message，并在同一个请求中接收 SSE 流：

```bash
curl -N -X POST \
  "$AGENT_BRIDGE_SERVER/api/v1/devices/DEVICE_ID/agents/AGENT_ID/sessions/SESSION_ID/messages" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"content":[{"type":"text","text":"解释当前目录的项目结构"}]}'
```

`v0.4.0` 只接受 `text` 内容块，单次 Message 文本总量最多为 128 KiB。`image` 等类型会返回 `UNSUPPORTED_CONTENT_TYPE`，超限文本返回 `PAYLOAD_TOO_LARGE`。

#### SSE 事件

| 事件 | 说明 |
| --- | --- |
| `message.delta` | 可展示的 Agent 文本增量 |
| `reasoning.delta` | Agent 提供的推理增量 |
| `session.updated` | 底层 Agent 自动刷新 Session ID |
| `done` | Message 正常结束 |
| `error` | 结构化错误码与可读信息 |

调用方断开 SSE 只会停止转发，不会终止本地 Agent。`v0.4.0` 暂不提供远程取消接口。

单次调用的推理与回答文本合计最多为 2 MiB。超过上限时会发送 `PAYLOAD_TOO_LARGE`，已接收的部分仍保存在 Device，Agent 进程不会因此断开。

<p align="center">
  <img src="docs/assets/readme/message-lifecycle.png" alt="Message 经过 Agent-Bridge 的调用流程" width="960" />
</p>

#### API 入口

| 地址 | 用途 |
| --- | --- |
| `/docs` | 可直接阅读的 Caller API 文档 |
| `/openapi.json` | OpenAPI 描述文件 |
| `/api/v1/devices` | Device 列表与在线状态 |
| `/api/v1/devices/{device_id}/agents` | Agent 列表 |
| `/api/v1/devices/{device_id}/agents/{agent_id}/sessions` | 查询或创建 Session |
| `/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages` | 查询 Message 或发送 SSE Message |

API Key 可以调用全部 Device，但不能访问 Pairing、API Key、Device 删除等管理接口。Key 不自动过期，可由 Owner 随时撤销。

直接使用 IP 和 HTTP 时，Owner Password、Pairing Code、API Key 与对话流量不会被 TLS 加密。`v0.4.0` 也未内置登录防爆破或 Caller API 限流，公开部署应在反向代理或云防火墙中补充限制。

### 运行与数据

#### Local 数据

Local 默认将配置与运行数据保存在 `~/.agent-bridge/`：

```text
~/.agent-bridge/
├── tunnel/config.json
├── agents/{agent_id}/sessions/
├── agents/{agent_id}/messages/
├── npm/                       # 自动安装的 ACP wrapper
└── logs/
```

`tunnel/config.json` 保留兼容字段 `bridge_id`、`token` 和 `server_url`。Session 与 Message 始终在 Device。macOS 与 Linux 会把目录限制为当前用户可访问，普通文件使用 `0600`，可执行文件使用 `0700`。

#### Server 数据

| 内容 | 路径 |
| --- | --- |
| 二进制 | `/usr/local/bin/agent-bridge-server` |
| 环境配置 | `/etc/agent-bridge/server.env` |
| SQLite 与备份 | `/var/lib/agent-bridge/` |
| systemd 服务 | `agent-bridge-server.service` |

重复运行安装脚本会先备份 SQLite，再升级二进制并重启；启动失败时会恢复上一版本。

#### Local 启动参数

```text
agent-bridge [--listen 127.0.0.1] [--port 9202] [--debug] [--background] [--version]
```

| 参数 | 说明 |
| --- | --- |
| `--listen` | 修改监听地址，默认且推荐使用 `127.0.0.1` |
| `--port` | 修改 Local Console 端口，默认 `9202` |
| `--debug` | 输出详细日志 |
| `--background` | 后台服务模式，不自动打开浏览器 |
| `--version` | 输出版本号后退出，不启动服务或注册自启动 |

Local Console 没有远程登录机制，不要将它监听到公网；远程调用应通过 Server 完成。

#### 卸载 Local

macOS / Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-local.sh | bash -s -- --uninstall
```

追加 `--purge` 会同时删除本地配置和历史：

```bash
curl -fsSL https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-local.sh | bash -s -- --uninstall --purge
```

Windows 先撤销当前用户的后台自启动，再删除下载的程序：

```powershell
.\agent-bridge.exe --uninstall
```

需要同时清除本地配置和历史时，再手动删除 `%USERPROFILE%\.agent-bridge`。

### 目录结构

```text
cmd/
├── bridge/                  # Agent-Bridge Local 入口与 Local Console
└── server/                  # Agent-Bridge Server 入口与 Remote Console
internal/
├── agent/                   # 11 个 Agent 适配器与 ACP 进程管理
├── protocol/                # ACP 与 Local-Server JSON-RPC 契约
├── service/                 # Local Session、Message 与远程连接用例
└── server/                  # Auth、Device、Gateway、Caller API 与 SQLite
web/
├── local/                   # Local Console
├── remote/                  # Remote Console
└── shared/                  # 共享组件、样式与中英文资源
scripts/                     # 构建、安装与验收脚本
```

### 从源码运行

要求 Go `1.25+`；修改 Console 时还需要 Node.js 与 npm。

```bash
git clone https://github.com/Zleap-AI/Agent-Bridge.git
cd Agent-Bridge

go run ./cmd/bridge
go run ./cmd/server serve --data-dir ./.agent-bridge-server
```

构建 Console 与全部 Release 二进制：

```bash
./scripts/build_web.sh
./scripts/build_release.sh v0.4.0
```

产物直接写入 `dist/`，不生成额外压缩包。

### 发布验证范围

每个版本标签只有在下面的自动化检查全部通过后才会创建 GitHub Release：

| 检查 | 实际验证方式 |
| --- | --- |
| Local：Linux x86_64 / ARM64 | 分别在原生 Linux runner 执行 `--version`、启动进程并请求 `/health` |
| Local：macOS Intel / Apple Silicon | 分别在原生 macOS runner 执行 `--version`、启动进程并请求 `/health` |
| Local：Windows x64 / ARM64 | 分别在原生 Windows runner 验证版本、启动、健康状态、当前用户自启动注册与卸载，并运行进程树与命令适配测试 |
| Server：Linux x86_64 / ARM64 | 分别在原生 Linux runner 执行 `version`、启动进程并请求 `/api/v1/status` |
| 全部发布文件 | 校验文件清单、Go 主包、GOOS/GOARCH、安装脚本语法和 `SHA256SUMS` |
| macOS / Linux 安装脚本事务 | 在隔离容器中模拟下载、服务管理、健康检查、升级与失败回滚 |

容器中的服务管理器是模拟实现，不能冒充真实系统验收。正式打标签前仍需在真实机器完成并记录：

- macOS Intel 与 Apple Silicon：LaunchAgent 安装、升级、重启后拉起和卸载。
- Linux x86_64 与 ARM64 代表性发行版：Local 的 systemd user 服务与 Server 的 systemd system 服务安装、升级、系统重启和卸载。
- Windows x64 与 ARM64：实际退出登录或重启后的自启动。

### 内部协议边界

- Local 与 Server 通过 JSON-RPC 2.0 over WebSocket 通讯。
- Local 保留 `X-Bridge-Id`、`X-Agent-Ids`、Bearer Bridge Token 和 `bridge/register` 契约。
- Server 通过 `invoke` 调用 Local；Local 再通过 ACP `session/new`、`session/load`、`session/prompt` 调用 Agent。
- 公开 Caller API 只使用 Device、Agent、Session 和 Message，不暴露内部协议命名。

### 开发检查

```bash
go test ./...
go test -race ./...
go vet ./...
```

Console：

```bash
cd web
npm ci
npm run typecheck
npm test
npx playwright install chromium
npm run test:e2e
npm run build
```

安装脚本会在无网络容器中模拟下载、服务管理、健康检查、升级与失败回滚：

```bash
./scripts/test_installers.sh
```

---

## 许可证

Agent-Bridge 基于 [MIT License](LICENSE) 开源。
