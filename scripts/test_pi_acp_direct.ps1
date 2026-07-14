# test_pi_acp_direct.ps1 - 直接 PowerShell 测试 pi-acp
# Lzm 2026-07-13

$pi_acp = "C:\Users\PC\.trae-cn\binaries\node\versions\24.18.0\pi-acp.cmd"

Write-Host "=== 1. 启动 pi-acp ===" -ForegroundColor Cyan
Write-Host "命令: $pi_acp" -ForegroundColor Gray

# 启动 pi-acp 进程
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $pi_acp
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$p = [System.Diagnostics.Process]::Start($psi)

$reader = New-Object System.IO.StreamReader($p.StandardOutput.BaseStream, [System.Text.Encoding]::UTF8)
$stderr_reader = New-Object System.IO.StreamReader($p.StandardError.BaseStream, [System.Text.Encoding]::UTF8)

function Send-And-Read($json, $timeoutMs=10000) {
    $line = ($json | ConvertTo-Json -Compress -Depth 10) + "`n"
    Write-Host "  -> $line" -NoNewline -ForegroundColor DarkYellow
    $p.StandardInput.Write($line)
    $p.StandardInput.Flush()

    $start = [DateTime]::UtcNow
    while (($now = [DateTime]::UtcNow) -lt $start.AddMilliseconds($timeoutMs)) {
        if (-not $reader.EndOfStream -and $reader.Peek() -ge 0) {
            $resp = $reader.ReadLine()
            Write-Host "  <- $resp" -ForegroundColor DarkGreen
            return $resp | ConvertFrom-Json
        }
        Start-Sleep -Milliseconds 100
    }
    Write-Host "  (timeout)" -ForegroundColor Red
    return $null
}

# 1. initialize
Write-Host "`n=== 2. initialize ===" -ForegroundColor Cyan
$r = Send-And-Read @{"jsonrpc"="2.0"; "id"="1"; "method"="initialize"; "params"=@{"protocolVersion"=2024; "capabilities"=@{}}} 10000
if (-not $r -or $r.error) {
    Write-Host "FAILED: $($r | ConvertTo-Json)" -ForegroundColor Red
    $p.Kill()
    exit 1
}
Write-Host "OK" -ForegroundColor Green

# 2. session/new
Write-Host "`n=== 3. session/new ===" -ForegroundColor Cyan
$r = Send-And-Read @{"jsonrpc"="2.0"; "id"="2"; "method"="session/new"; "params"=@{"cwd"=$pwd; "mcpServers"=@()}} 30000
if ($r -and $r.result -and $r.result.sessionId) {
    $sid = $r.result.sessionId
    Write-Host "OK sessionId: $($sid.Substring(0, [Math]::Min(40, $sid.Length)))..." -ForegroundColor Green
} elseif ($r -and $r.error) {
    Write-Host "FAILED: $($r.error | ConvertTo-Json)" -ForegroundColor Red
    $stderr = $stderr_reader.ReadToEnd()
    if ($stderr) { Write-Host "stderr: $stderr" -ForegroundColor Gray }
    $p.Kill()
    exit 1
} else {
    Write-Host "FAILED no result: $($r | ConvertTo-Json)" -ForegroundColor Red
    $p.Kill()
    exit 1
}

# 3. prompt
Write-Host "`n=== 4. session/prompt ===" -ForegroundColor Cyan
$r = Send-And-Read @{"jsonrpc"="2.0"; "id"="3"; "method"="session/prompt"; "params"=@{"sessionId"=$sid; "prompt"=@(@{"type"="text"; "text"="1+1=?"}) }} 60000
if ($r -and $r.result) {
    Write-Host "OK prompt success" -ForegroundColor Green
} elseif ($r -and $r.error) {
    Write-Host "FAILED: $($r.error | ConvertTo-Json)" -ForegroundColor Red
} else {
    Write-Host "WARN no response" -ForegroundColor Yellow
}

$p.Kill()
Start-Sleep 1
$stderr = $stderr_reader.ReadToEnd()
if ($stderr) {
    Write-Host "`nstderr: $stderr" -ForegroundColor Gray
}
Write-Host "`nDONE" -ForegroundColor Green
