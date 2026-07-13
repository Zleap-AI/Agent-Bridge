# -*- coding: utf-8 -*-
# PowerShell 5+
#
# test_e2e.ps1
# zleap-bridge 端到端综合测试脚本
# 测试：Bridge Admin API、Admin WebSocket 链路
# 连接 Bridge Admin（:9202），无需独立 SaaS 模拟器
#
# Lzm 2026-07-13

$ErrorActionPreference = "Stop"

# --- 配置 ---
$BRIDGE_ADMIN = "http://localhost:9202"
$ADMIN_WS = "ws://localhost:9202/ws/admin"
$ALL_TESTS = 0
$PASSED = 0
$FAILED = 0

function Test-Step($name, $script) {
    $global:ALL_TESTS++
    try {
        & $script
        Write-Host "  [PASS] $name" -ForegroundColor Green
        $global:PASSED++
    } catch {
        Write-Host "  [FAIL] $name : $_" -ForegroundColor Red
        $global:FAILED++
    }
}

function Get-Result {
    param($body)
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($body)
    return Invoke-RestMethod -Method Post -Uri "$BRIDGE_ADMIN/invoke" -Body $bytes -ContentType "application/json"
}

Write-Host "============================================" -ForegroundColor Cyan
Write-Host "  Zleap Bridge - 端到端综合测试" -ForegroundColor Cyan
Write-Host "============================================" -ForegroundColor Cyan
Write-Host ""

# ====== 测试集 1: Bridge Admin API ======
Write-Host "[集合 1] Bridge Admin API" -ForegroundColor Yellow

Test-Step "Health 检查" {
    $h = Invoke-RestMethod -Uri "$BRIDGE_ADMIN/health" -Method Get
    if ($h.status -ne "ok") { throw "status=$($h.status), 期望 ok" }
    if ($h.version -ne "0.3.0") { throw "version=$($h.version), 期望 0.3.0" }
    $ids = $h.agents.PSObject.Properties.Name -join ', '
    Write-Host "    agents: $ids"
}

Test-Step "Agent 列表" {
    $agents = Invoke-RestMethod -Uri "$BRIDGE_ADMIN/agents" -Method Get
    if ($agents.Count -lt 1) { throw "Agent 数量=${agents.Count}" }
    $ids = $agents | ForEach-Object { $_.agent_id }
    Write-Host "    检测到 $($agents.Count) 个 Agent: $($ids -join ', ')"
}

# ====== 测试集 2: Admin WebSocket 测试 ======
Write-Host "`n[集合 2] Admin WebSocket 链路测试" -ForegroundColor Yellow

Test-Step "WebSocket 连接 + ping" {
    $ws = New-Object System.Net.WebSockets.ClientWebSocket
    $cts = New-Object System.Threading.CancellationTokenSource(10000)
    $ws.ConnectAsync([System.Uri]$ADMIN_WS, $cts.Token).Wait()
    if ($ws.State -ne "Open") { throw "WebSocket 未打开: $($ws.State)" }

    # 读取欢迎消息 (bridge/list)
    $buf = New-Object byte[] 4096
    $res = $ws.ReceiveAsync((New-Object System.ArraySegment[byte] -ArgumentList @(,$buf)), $cts.Token)
    $res.Wait()
    $welcome = [System.Text.Encoding]::UTF8.GetString($buf, 0, $res.Result.Count)
    if ($welcome -notmatch 'bridge/list') { throw "欢迎消息异常: $($welcome.Substring(0, 80))" }
    Write-Host "    bridge/list 欢迎 OK"

    # 发送 ping
    $ping = '{"id":"test_ping","method":"ping"}'
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($ping)
    $seg = New-Object System.ArraySegment[byte] -ArgumentList @(,$bytes)
    $ws.SendAsync($seg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, $cts.Token).Wait()

    # 读取响应
    $buf = New-Object byte[] 4096
    $res = $ws.ReceiveAsync((New-Object System.ArraySegment[byte] -ArgumentList @(,$buf)), $cts.Token)
    $res.Wait()
    $resp = [System.Text.Encoding]::UTF8.GetString($buf, 0, $res.Result.Count)
    if ($resp -notmatch '"pong"') { throw "ping 响应异常: $resp" }
    Write-Host "    ping/pong OK"

    $ws.Dispose()
    Write-Host "    WebSocket 关闭 OK"
}

# ====== 整体结果 ======
Write-Host ""
Write-Host "============================================" -ForegroundColor Cyan
Write-Host " 测试完成: $ALL_TESTS 总用例" -ForegroundColor White
if ($FAILED -eq 0) {
    Write-Host " 全部通过 ($PASSED/$ALL_TESTS)" -ForegroundColor Green
} else {
    Write-Host " 通过: $PASSED, 失败: $FAILED" -ForegroundColor Yellow
}
Write-Host "============================================" -ForegroundColor Cyan
