# -*- coding: utf-8 -*-
# PowerShell 5.1+
#
# install-local.ps1
# Agent-Bridge Local Windows 安装脚本
# 自动下载最新 Release、安装到用户目录、注册开机自启
# 支持一键安装 / 卸载 / 清除
#
# 用法:
#   # 在线安装（推荐）
#   iwr -useb https://raw.githubusercontent.com/Zleap-AI/Agent-Bridge/main/scripts/install-local.ps1 | iex
#
#   # 本地运行
#   .\scripts\install-local.ps1
#
#   # 卸载
#   .\scripts\install-local.ps1 -Uninstall
#
#   # 卸载并清除数据
#   .\scripts\install-local.ps1 -Uninstall -Purge
#
# 环境变量:
#   AGENT_BRIDGE_VERSION    指定版本号（默认 latest，如 v0.4.0）
#   AGENT_BRIDGE_LOCAL_URL  自定义 Local Console 地址（默认 http://localhost:9202）
#
# Lzm 2026-07-23

[CmdletBinding()]
param(
    [switch]$Uninstall,
    [switch]$Purge
)

# ─── 常量 ─────────────────────────────────────────────────
$repo = if ($env:AGENT_BRIDGE_REPOSITORY) { $env:AGENT_BRIDGE_REPOSITORY } else { "Zleap-AI/Agent-Bridge" }
$version = if ($env:AGENT_BRIDGE_VERSION) { $env:AGENT_BRIDGE_VERSION } else { "latest" }
$localUrl = if ($env:AGENT_BRIDGE_LOCAL_URL) { $env:AGENT_BRIDGE_LOCAL_URL } else { "http://localhost:9202" }

$installRoot = "$env:LOCALAPPDATA\Agent-Bridge"
$binDir = "$installRoot\bin"
$dataDir = "$env:USERPROFILE\.agent-bridge"
$binaryName = "agent-bridge.exe"
$binaryPath = "$binDir\$binaryName"
$healthUrl = "$($localUrl.TrimEnd('/'))/health"
$serviceLabel = "Agent-Bridge"

# ─── 辅助函数 ─────────────────────────────────────────────

function Log($msg) {
    Write-Host "[Agent-Bridge] $msg" -ForegroundColor Cyan
}

function Warn($msg) {
    Write-Host "[Agent-Bridge] WARNING: $msg" -ForegroundColor Yellow
}

function Fail($msg) {
    Write-Host "[Agent-Bridge] ERROR: $msg" -ForegroundColor Red
    exit 1
}

# 获取最新 Release 版本号
function Get-LatestVersion {
    $latestUrl = "https://github.com/$repo/releases/latest"
    try {
        $request = [System.Net.WebRequest]::Create($latestUrl)
        $request.AllowAutoRedirect = $false
        $response = $request.GetResponse()
        $location = $response.Headers["Location"]
        $response.Close()
        if ($location -and $location -match '\/tag\/(v[^\/]+)') {
            return $matches[1]
        }
    } catch {
        # 降级：用 curl 尝试
        try {
            $result = & "curl.exe" -fsSLI -o /dev/null -w '%{url_effective}' $latestUrl 2>$null
            if ($result -match '\/tag\/(v[^\/]+)') {
                return $matches[1]
            }
        } catch {
            # 继续降级
        }
    }
    return $null
}

# 计算文件 SHA256
function Get-FileSha256($path) {
    return (Get-FileHash -Path $path -Algorithm SHA256).Hash.ToLower()
}

# 判断系统架构
function Get-Arch {
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($arch -eq "ARM64") { return "arm64" }
    # amd64 或 x86 都走 amd64
    return "amd64"
}

# ─── 卸载逻辑 ─────────────────────────────────────────────

if ($Uninstall) {
    Log "正在卸载 Agent-Bridge Local ..."

    # 停止进程（如果正在运行）
    $proc = Get-Process -Name "agent-bridge" -ErrorAction SilentlyContinue
    if ($proc) {
        Log "正在停止运行中的 Agent-Bridge 进程 ..."
        $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    }

    # 使用程序自带的 --uninstall 清理注册表自启
    if (Test-Path $binaryPath) {
        Log "执行程序自卸载 ..."
        try {
            $uninstallResult = & $binaryPath --uninstall 2>&1
            Log $uninstallResult
        } catch {
            Warn "程序自卸载执行异常: $_"
        }
    }

    # 清理安装目录
    if (Test-Path $binDir) {
        Log "删除二进制目录: $binDir"
        Remove-Item -Recurse -Force $binDir -ErrorAction SilentlyContinue
    }

    # 清理 PATH 中的条目
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($currentPath -like "*$binDir*") {
        $newPath = ($currentPath -split ';' | Where-Object { $_ -ne $binDir }) -join ';'
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        Log "已从用户 PATH 中移除 $binDir"
    }

    # --Purge 时清除数据目录
    if ($Purge -and (Test-Path $dataDir)) {
        Log "清除数据目录: $dataDir"
        Remove-Item -Recurse -Force $dataDir -ErrorAction SilentlyContinue
    }

    # 清理空安装根目录
    if ((Test-Path $installRoot) -and -not (Get-ChildItem $installRoot -ErrorAction SilentlyContinue)) {
        Remove-Item -Force $installRoot -ErrorAction SilentlyContinue
    }

    Log "Agent-Bridge Local 卸载完成"
    return
}

# ─── 前置检查 ─────────────────────────────────────────────

# 检测架构
$arch = Get-Arch
Log "系统架构: $arch"

# 解析最新版本
$resolvedVersion = $version
if ($version -eq "latest") {
    Log "正在查询最新 Release 版本 ..."
    $resolvedVersion = Get-LatestVersion
    if (-not $resolvedVersion) {
        Fail "无法解析最新版本号，可设置 AGENT_BRIDGE_VERSION 手动指定"
    }
    Log "最新版本: $resolvedVersion"
}
if ($resolvedVersion -notmatch '^v') {
    $resolvedVersion = "v$resolvedVersion"
}

$expectedVersion = $resolvedVersion.TrimStart('v')

# 构建下载 URL
# Release 产物命名规则见 build_release.ps1:
#   zleap-bridge-go_v{version}_windows_{arch}.exe
$assetName = "zleap-bridge-go_${resolvedVersion}_windows_${arch}.exe"
$baseUrl = "https://github.com/${repo}/releases/download/${resolvedVersion}"
$downloadUrl = "${baseUrl}/${assetName}"
$checksumUrl = "${baseUrl}/SHA256SUMS"

# ─── 下载与校验 ────────────────────────────────────────────

# 创建安装目录
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

$tmpDir = "$env:TEMP\agent-bridge-install"
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

$tmpBinary = "$tmpDir\$assetName"
$tmpChecksum = "$tmpDir\SHA256SUMS"
$binaryExisted = Test-Path $binaryPath
$wasRunning = $false

try {
    # 检查是否已有运行中的进程
    $existingProc = Get-Process -Name "agent-bridge" -ErrorAction SilentlyContinue
    if ($existingProc) {
        $wasRunning = $true
        Log "检测到正在运行的 Agent-Bridge，安装期间将停止 ..."
        $existingProc | Stop-Process -Force -ErrorAction SilentlyContinue
        # 等待进程退出
        Start-Sleep -Seconds 1
    }

    # 备份旧二进制
    if ($binaryExisted) {
        Log "备份现有二进制 ..."
        Copy-Item -Path $binaryPath -Destination "$tmpDir\previous-binary.exe" -Force
    }

    # 下载二进制
    Log "正在下载 $assetName ..."
    try {
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($downloadUrl, $tmpBinary)
    } catch {
        # 降级到 curl
        & "curl.exe" -fsSL --retry 3 --connect-timeout 15 $downloadUrl -o $tmpBinary 2>$null
        if (-not (Test-Path $tmpBinary) -or ((Get-Item $tmpBinary).Length -eq 0)) {
            Fail "下载失败: $downloadUrl"
        }
    }

    # 下载 SHA256 校验文件
    Log "正在下载 SHA256SUMS ..."
    try {
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($checksumUrl, $tmpChecksum)
    } catch {
        & "curl.exe" -fsSL --retry 3 --connect-timeout 15 $checksumUrl -o $tmpChecksum 2>$null
        if (-not (Test-Path $tmpChecksum)) {
            Fail "下载 SHA256SUMS 失败: $checksumUrl"
        }
    }

    # 校验 SHA256
    $actualHash = Get-FileSha256 $tmpBinary
    $expectedHash = $null
    Get-Content $tmpChecksum | ForEach-Object {
        if ($_ -match '^([a-f0-9]+)\s+' -and $_ -like "*$assetName*") {
            $expectedHash = $matches[1]
        }
    }
    if (-not $expectedHash) {
        Warn "SHA256SUMS 中找不到 $assetName 的校验值，跳过校验"
    } elseif ($actualHash -ne $expectedHash) {
        # 尝试带 * 前缀的格式
        Get-Content $tmpChecksum | ForEach-Object {
            if ($_ -match '^([a-f0-9]+)\s+\*?' -and $_ -like "*$assetName*") {
                $expectedHash = $matches[1]
            }
        }
        if ($actualHash -ne $expectedHash) {
            Fail "SHA256 校验失败！期望: $expectedHash，实际: $actualHash"
        }
    }
    Log "SHA256 校验通过"

    # ─── 安装二进制 ──────────────────────────────────────

    Log "安装二进制到 $binaryPath ..."
    Copy-Item -Path $tmpBinary -Destination $binaryPath -Force

    # ─── 添加 PATH ───────────────────────────────────────

    $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($currentPath -notlike "*$binDir*") {
        $newPath = if ($currentPath) { "$currentPath;$binDir" } else { $binDir }
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        Log "已将 $binDir 添加到用户 PATH"
    }

    # ─── 启动并注册自启 ──────────────────────────────────

    Log "首次启动 Agent-Bridge Local（将自动注册开机自启）..."
    $proc = Start-Process -FilePath $binaryPath -ArgumentList "--background" `
        -WindowStyle Hidden -PassThru -RedirectStandardOutput "$tmpDir\first-run.log" `
        -RedirectStandardError "$tmpDir\first-run-err.log"

    # 等待健康检查
    Log "等待服务就绪（最长 120 秒）..."
    $healthy = $false
    for ($i = 0; $i -lt 120; $i++) {
        Start-Sleep -Seconds 1
        try {
            $healthResponse = & "curl.exe" -fsS --connect-timeout 2 --max-time 4 $healthUrl 2>$null
            if (-not $healthResponse) {
                $healthResponse = (Invoke-WebRequest -Uri $healthUrl -UseBasicParsing -TimeoutSec 4 -ErrorAction SilentlyContinue).Content
            }
            if ($healthResponse -match '"status"\s*:\s*"ok"' -and $healthResponse -match '"version"\s*:\s*"([^"]+)"') {
                $reportedVersion = $matches[1]
                if ($reportedVersion -eq $expectedVersion) {
                    $healthy = $true
                    break
                }
            }
        } catch {
            # 继续等待
        }
    }

    if (-not $healthy) {
        # 回滚
        Log "安装失败，正在回滚 ..."

        # 停止失败的进程
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue

        if ($binaryExisted) {
            Copy-Item -Path "$tmpDir\previous-binary.exe" -Destination $binaryPath -Force
            Log "已恢复旧版本二进制"
        } else {
            Remove-Item -Path $binaryPath -Force -ErrorAction SilentlyContinue
            # 从 PATH 移除
            $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
            if ($currentPath -like "*$binDir*") {
                $newPath = ($currentPath -split ';' | Where-Object { $_ -ne $binDir }) -join ';'
                [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            }
        }

        # 如果之前就在运行，重启旧版本
        if ($wasRunning -and $binaryExisted) {
            Start-Process -FilePath $binaryPath -ArgumentList "--background" -WindowStyle Hidden
            Log "已重启旧版本 Agent-Bridge"
        }

        # 输出错误日志
        if (Test-Path "$tmpDir\first-run.log") {
            $logContent = Get-Content "$tmpDir\first-run.log" -Tail 60
            if ($logContent) { Write-Host "--- 启动日志 ---" -ForegroundColor Yellow; $logContent }
        }
        if (Test-Path "$tmpDir\first-run-err.log") {
            $logContent = Get-Content "$tmpDir\first-run-err.log" -Tail 60
            if ($logContent) { Write-Host "--- 错误日志 ---" -ForegroundColor Yellow; $logContent }
        }

        Fail "服务未能在 120 秒内就绪，已回滚"
    }

    # ─── 完成 ─────────────────────────────────────────────

    Log "Agent-Bridge Local $resolvedVersion 安装完成！"
    Log "Local Console: $localUrl"
    Log "二进制路径: $binaryPath"
    Log "配置文件: $env:USERPROFILE\.agent-bridge\tunnel\config.json"
    Log ""

    # 打开 Local Console
    try {
        Start-Process "http://$($localUrl -replace 'https?://','')"
    } catch {
        # 打开浏览器失败也没关系
    }

} finally {
    # 清理临时文件
    if (Test-Path $tmpDir) {
        Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
    }
}
