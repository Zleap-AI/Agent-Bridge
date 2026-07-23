#requires -Version 5.1

[CmdletBinding()]
param(
    [string]$Version = "v0.5.0"
)

$ErrorActionPreference = "Stop"
if ($Version -notmatch '^v[A-Za-z0-9._-]+$') {
    throw "Version must be a release tag such as v0.5.0"
}

$binaryVersion = $Version.TrimStart('v')
$rootDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$distDir = Join-Path $rootDir "dist"
$artifacts = [System.Collections.Generic.List[string]]::new()

if (Test-Path $distDir) {
    Remove-Item -Recurse -Force $distDir
}
New-Item -ItemType Directory -Force -Path $distDir | Out-Null

Push-Location $rootDir
try {
    if (Test-Path (Join-Path $rootDir "web\package.json")) {
        Push-Location (Join-Path $rootDir "web")
        try {
            if (-not (Test-Path "node_modules")) {
                & npm.cmd ci
                if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
            }
            & npm.cmd run build
            if ($LASTEXITCODE -ne 0) { throw "Web build failed" }
        } finally {
            Pop-Location
        }

        Copy-Item (Join-Path $rootDir "web\dist\local\index.html") `
            (Join-Path $rootDir "cmd\bridge\html\index.html") -Force
        Copy-Item (Join-Path $rootDir "web\dist\remote\index.html") `
            (Join-Path $rootDir "cmd\server\html\index.html") -Force
    }

    $localTargets = @(
        @{ OS = "darwin";  Arch = "amd64" },
        @{ OS = "darwin";  Arch = "arm64" },
        @{ OS = "linux";   Arch = "amd64" },
        @{ OS = "linux";   Arch = "arm64" },
        @{ OS = "windows"; Arch = "amd64" },
        @{ OS = "windows"; Arch = "arm64" }
    )

    foreach ($target in $localTargets) {
        $name = "agent-bridge_${Version}_$($target.OS)_$($target.Arch)"
        if ($target.OS -eq "windows") {
            $name += ".exe"
        }

        $env:CGO_ENABLED = "0"
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        & go build -trimpath "-ldflags=-s -w -X main.version=${binaryVersion}" `
            -o (Join-Path $distDir $name) ./cmd/bridge
        if ($LASTEXITCODE -ne 0) {
            throw "Local build failed for $($target.OS)/$($target.Arch)"
        }
        $artifacts.Add($name)
    }

    foreach ($arch in @("amd64", "arm64")) {
        $name = "agent-bridge-server_${Version}_linux_${arch}"
        $env:CGO_ENABLED = "0"
        $env:GOOS = "linux"
        $env:GOARCH = $arch
        & go build -trimpath "-ldflags=-s -w -X main.version=${binaryVersion}" `
            -o (Join-Path $distDir $name) ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            throw "Server build failed for linux/${arch}"
        }
        $artifacts.Add($name)
    }

    foreach ($installer in @("install-local.sh", "install-local.ps1", "install-server.sh")) {
        Copy-Item (Join-Path $rootDir "scripts\$installer") `
            (Join-Path $distDir $installer) -Force
        $artifacts.Add($installer)
    }

    $checksums = foreach ($name in $artifacts) {
        $hash = (Get-FileHash (Join-Path $distDir $name) -Algorithm SHA256).Hash.ToLowerInvariant()
        "${hash}  ${name}"
    }
    Set-Content -Path (Join-Path $distDir "SHA256SUMS") -Value $checksums -Encoding Ascii

    Write-Host "Release binaries written to $distDir" -ForegroundColor Green
} finally {
    Pop-Location
}
