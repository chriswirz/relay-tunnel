<#
.SYNOPSIS
  Build relay-tunnel.

.DESCRIPTION
  With no arguments it builds for this machine into relay-tunnel.exe; pass -All
  to cross-compile every release target into dist\, which is what the GitHub
  Actions workflow does; pass -Test to run gofmt, go vet and go test.

.EXAMPLE
  .\build.ps1
  .\build.ps1 -All
  .\build.ps1 -Test
#>
[CmdletBinding()]
param(
    [switch]$All,
    [switch]$Test,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

$pkg = './cmd/relay-tunnel'

# Determine version from git if available; fall back to "dev". Guard against
# git's stderr tripping the Stop preference (and against not being a repo).
$version = 'dev'
$prev = $ErrorActionPreference
$ErrorActionPreference = 'SilentlyContinue'
try {
    $desc = git describe --tags --always 2>$null
    if ($LASTEXITCODE -eq 0 -and $desc) { $version = "$desc".Trim() }
} catch { }
$ErrorActionPreference = $prev
$ldflags = "-s -w -X main.version=$version"

function Build-One {
    param([string]$Goos, [string]$Goarch, [string]$Ext = '')
    $out = "dist/relay-tunnel-$Goos-$Goarch$Ext"
    Write-Host "  $out"
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags $ldflags -o $out $pkg
    if ($LASTEXITCODE -ne 0) { throw "build failed for $Goos/$Goarch" }
}

if ($Help) {
    Write-Host 'Usage: .\build.ps1 [-All | -Test | -Help]'
    Write-Host '  (no args)  build relay-tunnel.exe for this machine'
    Write-Host '  -All       cross-compile every release target into dist\'
    Write-Host '  -Test      gofmt, go vet and go test'
    return
}

if ($Test) {
    gofmt -l .
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
    return
}

if ($All) {
    Write-Host "Building relay-tunnel $version for all targets"
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    try {
        Build-One windows amd64 '.exe'
        Build-One windows arm64 '.exe'
        Build-One linux   amd64
        Build-One linux   arm64
        Build-One darwin  amd64
        Build-One darwin  arm64
    }
    finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
    $sumsPath = 'dist/SHA256SUMS'
    if (Test-Path $sumsPath) { Remove-Item $sumsPath }
    Get-ChildItem dist -Filter 'relay-tunnel-*' | ForEach-Object {
        $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
        "$h  $($_.Name)" | Add-Content -Encoding utf8 $sumsPath
    }
    Write-Host 'Wrote dist/SHA256SUMS'
    return
}

# Default: host build.
Write-Host "Building relay-tunnel $version"
$env:CGO_ENABLED = '0'
try {
    go build -trimpath -ldflags $ldflags -o relay-tunnel.exe $pkg
    if ($LASTEXITCODE -ne 0) { throw 'build failed' }
}
finally {
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
Write-Host 'Wrote relay-tunnel.exe'
