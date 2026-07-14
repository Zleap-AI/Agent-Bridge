# Agent-Bridge

Agent-Bridge 是一套让远程调用方安全访问用户本地 AI Agent 的自托管系统，同时保留不依赖服务器的本地使用能力。

## Language

**Agent-Bridge Local**:
运行在用户电脑上的 Agent-Bridge 本地端程序，连接本地 Agent 与 Agent-Bridge Server，并保留不依赖服务端的本地使用能力。
_Avoid_: Edge Bridge、本地服务器、远程客户端

**Agent-Bridge Server**:
部署在公网服务器上的完整服务端产品，包含远程连接、远程控制台和开发者调用入口。
_Avoid_: SaaS、云端 Bridge

**Server Owner**:
拥有并管理一套 Agent-Bridge Server 部署的个人或团队；第一版中每套部署只有一个 Server Owner。
_Avoid_: Tenant、平台用户

**Owner Password**:
第一版中用于进入 Remote Console 的唯一管理密码，不对应用户名或账号体系。
_Avoid_: 用户账号、Device Credential、API Key

**Setup Token**:
Agent-Bridge Server 首次启动时生成、仅用于设置 Owner Password 且成功后立即失效的一次性凭证。
_Avoid_: Owner Password、Pairing Code

**Gateway**:
Agent-Bridge Server 中负责关联远程调用方与在线 Agent-Bridge Local 的连接中枢。
_Avoid_: Tunnel Server、中转脚本

**Local Console**:
由 Agent-Bridge Local 提供的本地管理与调试界面，只管理当前设备。
_Avoid_: Remote Console、远程连接页面

**Remote Console**:
由 Agent-Bridge Server 提供的网页界面，用于查看并调用已连接的远程设备及其 Agent。
_Avoid_: Local Console、测试页面

**Caller API**:
Agent-Bridge Server 面向开发者提供的远程调用接口，允许第三方应用集成本地 Agent 能力。
_Avoid_: Admin API、本地 API

**API Key**:
Server Owner 为 Caller API 创建的独立、可撤销调用凭证，可使用完整 Caller API 并调用当前 Server 下的全部 Device，但不授予 Remote Console 管理权限。
_Avoid_: Owner Password、Device Credential

**Device**:
运行一个 Agent-Bridge Local 实例并可被远程识别的用户电脑；Remote Console 与 Caller API 以 Device 表达它，公开 `id` 沿用现有 Bridge ID 的值。
_Avoid_: Client、Host、公开 API 中的 Bridge

**Bridge ID**:
Agent-Bridge Local 保存在本地配置并在远程连接时上报的稳定标识；可以手工配置，也可以由 Pairing 自动写入，在公开 API 中作为 Device 的 `id` 返回。
_Avoid_: Device ID、hostname

**Device Name**:
Device 在 Remote Console 中的可编辑显示名称；Pairing 时默认使用电脑的 hostname，但不参与身份识别，也不改变公开 `id` 或 Bridge ID。
_Avoid_: Device ID、Bridge ID

**Agent**:
安装在 Device 上、由 Agent-Bridge Local 通过 ACP 调用的 AI Agent 程序。

**Pairing**:
Server Owner 使用短期 Pairing Code，将一个 Device 可信地绑定到 Agent-Bridge Server 的过程。
_Avoid_: 登录、注册 Bridge

**Pairing Code**:
由 Agent-Bridge Server 签发、短期有效且只能使用一次的设备绑定凭证。
_Avoid_: Device Token、邀请码

**Bridge Token**:
Agent-Bridge Local 保存在本地配置并通过 Bearer 认证发送的长期可撤销凭证；Pairing 只负责自动签发和写入，不改变现有连接格式。
_Avoid_: Pairing Code、Owner Password、API Key

**Remote Invocation**:
由 Remote Console 或 Caller API 发起，经 Gateway 路由到指定 Device 和 Agent 的调用。
_Avoid_: 远程桌面、直接连接本地端口

**Session**:
由某个 Device 上的指定 Agent 持有的一段连续交互上下文。
_Avoid_: Conversation、Thread

**Message**:
在 Session 中由调用方发送或由 Agent 返回的一组有序内容块；第一版公开 API 只支持 `text` 内容块。
_Avoid_: Prompt、Query

**Conversation Data**:
Remote Invocation 涉及的 Message 和 Session 历史，由 Device 持有；第一版 Agent-Bridge Server 只转发而不持久化正文。
_Avoid_: Server History、云端会话

**Invocation Metadata**:
Agent-Bridge Server 为诊断调用链路保存的不含对话正文的信息，例如调用时间、目标 Device、Agent、结果状态和耗时。
_Avoid_: Conversation Data、完整请求日志
