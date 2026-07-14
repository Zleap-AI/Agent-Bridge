param(
    [Parameter(Mandatory = $true)]
    [string]$DistDir,
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [Parameter(Mandatory = $true)]
    [ValidateSet("amd64", "arm64")]
    [string]$Arch
)

$ErrorActionPreference = "Stop"

function Fail([string]$Message) {
    throw "[release-smoke] $Message"
}

$expectedArchitecture = if ($Arch -eq "amd64") { "x64" } else { "arm64" }
$actualArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
if ($actualArchitecture -ne $expectedArchitecture) {
    Fail "runner architecture is $actualArchitecture, expected $expectedArchitecture"
}
if ($Version -notmatch '^v[A-Za-z0-9._-]+$') {
    Fail "invalid release version: $Version"
}

$binaryVersion = $Version.TrimStart("v")
$resolvedDist = (Resolve-Path $DistDir).Path
$binary = Join-Path $resolvedDist "agent-bridge_${Version}_windows_${Arch}.exe"
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    Fail "missing Local artifact: $binary"
}

$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("agent-bridge-smoke-" + [guid]::NewGuid().ToString("N"))
$homeDir = Join-Path $tempDir "home"
$stdoutLog = Join-Path $tempDir "local.stdout.log"
$stderrLog = Join-Path $tempDir "local.stderr.log"
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$originalHome = $env:HOME
$originalUserProfile = $env:USERPROFILE
$originalRunValue = $null
$originalRunKind = $null
$hadOriginalRunValue = $false
$process = $null

try {
    if (Test-Path -LiteralPath $runKey) {
        $runKeyItem = Get-Item -LiteralPath $runKey -ErrorAction Stop
        if ($runKeyItem.GetValueNames() -contains "Agent-Bridge") {
            $originalRunValue = $runKeyItem.GetValue(
                "Agent-Bridge",
                $null,
                [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames
            )
            $originalRunKind = $runKeyItem.GetValueKind("Agent-Bridge")
            $hadOriginalRunValue = $true
        }
    }

    New-Item -ItemType Directory -Force -Path $homeDir | Out-Null
    $env:HOME = $homeDir
    $env:USERPROFILE = $homeDir
    Remove-ItemProperty -Path $runKey -Name "Agent-Bridge" -ErrorAction SilentlyContinue

    $reportedVersion = ((& $binary --version) | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        Fail "Agent-Bridge Local --version exited with $LASTEXITCODE"
    }
    if ($reportedVersion -ne $binaryVersion) {
        Fail "Agent-Bridge Local reported version '$reportedVersion', expected '$binaryVersion'"
    }

    $port = 39202
    $process = Start-Process -FilePath $binary `
        -ArgumentList @("--background", "--listen", "127.0.0.1", "--port", "$port") `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog -PassThru

    $health = $null
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $process.Refresh()
        if ($process.HasExited) {
            Fail "Agent-Bridge Local exited before becoming healthy"
        }
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$port/health" -TimeoutSec 2
            break
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    if ($null -eq $health) {
        Fail "Agent-Bridge Local did not become healthy"
    }
    if ($health.status -ne "ok" -or $health.version -ne $binaryVersion) {
        Fail "Agent-Bridge Local returned unexpected health payload: $($health | ConvertTo-Json -Compress)"
    }

    $registeredCommand = Get-ItemPropertyValue -Path $runKey -Name "Agent-Bridge" -ErrorAction Stop
    $expectedCommand = '"' + $binary + '" --background'
    if ($registeredCommand -ne $expectedCommand) {
        Fail "autostart command is '$registeredCommand', expected '$expectedCommand'"
    }

    Stop-Process -Id $process.Id -Force
    $process.WaitForExit()
    $process = $null

    & $binary --uninstall | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Fail "Agent-Bridge Local --uninstall exited with $LASTEXITCODE"
    }
    $remainingCommand = Get-ItemPropertyValue -Path $runKey -Name "Agent-Bridge" -ErrorAction SilentlyContinue
    if ($null -ne $remainingCommand) {
        Fail "Agent-Bridge Local autostart entry still exists after --uninstall"
    }

    Write-Host "[release-smoke] Local windows/$Arch reports $binaryVersion, starts, registers autostart, and uninstalls"
} finally {
    if ($null -ne $process) {
        try {
            $process.Refresh()
            if (-not $process.HasExited) {
                Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
                $process.WaitForExit()
            }
        } catch {
            Write-Warning "could not stop smoke process: $_"
        }
    }
    try {
        & $binary --uninstall | Out-Null
    } catch {
        Remove-ItemProperty -Path $runKey -Name "Agent-Bridge" -ErrorAction SilentlyContinue
    }
    Remove-ItemProperty -Path $runKey -Name "Agent-Bridge" -ErrorAction SilentlyContinue
    if ($hadOriginalRunValue) {
        New-Item -Path $runKey -Force | Out-Null
        $runKeyItem = Get-Item -LiteralPath $runKey -ErrorAction Stop
        $runKeyItem.SetValue("Agent-Bridge", $originalRunValue, $originalRunKind)
    }
    if (Test-Path -LiteralPath $stderrLog) {
        $stderrText = Get-Content -Raw -LiteralPath $stderrLog
        if ($stderrText) {
            Write-Host $stderrText
        }
    }
    $env:HOME = $originalHome
    $env:USERPROFILE = $originalUserProfile
    Remove-Item -Recurse -Force -LiteralPath $tempDir -ErrorAction SilentlyContinue
}
