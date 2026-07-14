# -*- coding: utf-8 -*-
# PowerShell 5.1+
#
# build_release.ps1
# zleap-bridge-go 多平台交叉编译打包脚本
# 输出 zip 包到 dist/ 目录
#
# Lzm 2026-07-14

param(
    [string]$version = "v0.4.0"
)

$ErrorActionPreference = "Stop"
$binaryVersion = $version.TrimStart('v')
$rootDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$distDir = Join-Path $rootDir "dist"

# 清理旧的 dist 目录
if (Test-Path $distDir) {
    Remove-Item -Recurse -Force $distDir
}
New-Item -ItemType Directory -Force -Path $distDir | Out-Null

Set-Location $rootDir

# 目标平台配置
$targets = @(
    @{ os = "windows"; arch = "amd64" },
    @{ os = "windows"; arch = "arm64" },
    @{ os = "linux";   arch = "amd64" },
    @{ os = "linux";   arch = "arm64" },
    @{ os = "darwin";  arch = "amd64" },
    @{ os = "darwin";  arch = "arm64" }
)

foreach ($target in $targets) {
    $goos = $target.os
    $goarch = $target.arch

    # --- Local (bridge) ---
    if ($goos -eq "windows") {
        $binaryName = "zleap-bridge-go_${version}_${goos}_${goarch}.exe"
    } else {
        $binaryName = "zleap-bridge-go_${version}_${goos}_${goarch}"
    }

    Write-Host "==> 构建 Local ${goos}/${goarch} ..." -ForegroundColor Cyan

    $env:CGO_ENABLED = "0"
    $env:GOOS = $goos
    $env:GOARCH = $goarch

    go build -trimpath -ldflags="-s -w -X main.version=${binaryVersion}" `
        -o (Join-Path $distDir $binaryName) ./cmd/bridge

    if ($LASTEXITCODE -ne 0) {
        Write-Host "!! Local 构建失败 ${goos}/${goarch}" -ForegroundColor Red
        exit 1
    }

    Write-Host "   => $binaryName" -ForegroundColor Green

    # --- Server（仅 Linux，服务端部署目标） ---
    if ($goos -eq "linux") {
        $serverName = "zleap-bridge-go_server_${version}_${goos}_${goarch}"

        Write-Host "==> 构建 Server ${goos}/${goarch} ..." -ForegroundColor Cyan

        go build -trimpath -ldflags="-s -w -X main.version=${binaryVersion}" `
            -o (Join-Path $distDir $serverName) ./cmd/server

        if ($LASTEXITCODE -ne 0) {
            Write-Host "!! Server 构建失败 ${goos}/${goarch}" -ForegroundColor Red
            exit 1
        }

        Write-Host "   => $serverName" -ForegroundColor Green
    }
}

Write-Host "`n==> 生成校验文件 ..." -ForegroundColor Cyan

# 生成 SHA256 校验
$checksumFile = Join-Path $distDir "SHA256SUMS"
Get-ChildItem "$distDir\zleap-bridge-go_*" | ForEach-Object {
    $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
    "$hash  $($_.Name)" | Out-File -FilePath $checksumFile -Encoding ascii -Append
}

Write-Host "   => SHA256SUMS" -ForegroundColor Green

Write-Host "`n==> 打包 zip ..." -ForegroundColor Cyan

# 按平台打包 zip（Server 二进制合并到对应 Linux zip 中）
$zipGroups = @(
    @{ suffix = "windows_amd64"; files = @("zleap-bridge-go_${version}_windows_amd64.exe") }
    @{ suffix = "windows_arm64"; files = @("zleap-bridge-go_${version}_windows_arm64.exe") }
    @{ suffix = "darwin_amd64";  files = @("zleap-bridge-go_${version}_darwin_amd64") }
    @{ suffix = "darwin_arm64";  files = @("zleap-bridge-go_${version}_darwin_arm64") }
    @{ suffix = "linux_amd64";   files = @("zleap-bridge-go_${version}_linux_amd64", "zleap-bridge-go_server_${version}_linux_amd64") }
    @{ suffix = "linux_arm64";   files = @("zleap-bridge-go_${version}_linux_arm64", "zleap-bridge-go_server_${version}_linux_arm64") }
)

foreach ($group in $zipGroups) {
    $suffix = $group.suffix
    $zipName = "zleap-bridge-go_${version}_${suffix}.zip"
    $zipPath = Join-Path $distDir $zipName

    $fullPaths = $group.files | ForEach-Object { Join-Path $distDir $_ }
    Compress-Archive -Path $fullPaths -DestinationPath $zipPath -Force
    Write-Host "   => $zipName" -ForegroundColor Green

    # 清理临时 zip 文件（如果 Server 在 SHA 阶段被重复计算）
    $existingZip = Join-Path $distDir "zleap-bridge-go_${version}_${suffix}.zip"
    if (-not (Test-Path $existingZip)) { return }
}

Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host "构建完成！输出目录: $distDir" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Cyan
