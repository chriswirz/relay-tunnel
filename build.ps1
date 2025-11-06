<#
.SYNOPSIS
  Build relay-tunnel.

.DESCRIPTION
  With no arguments it builds for this machine into relay-tunnel.exe; pass -All
  to cross-compile every release target into dist\, which is what the GitHub
  Actions workflow does; pass -Test to run gofmt, go vet and go test.

  The admin web interface is a separate build: -Web exports the Next.js
  application in web\ into internal\webui\out, where the Go build embeds it. A
  binary built without that step runs fine and serves the API alone, so the
  frontend is only rebuilt when asked for, or as part of -All.

  Everything under examples\ is a program of its own, built alongside the CLI by
  -All (into dist\examples, which is what the release attaches) and on its own by
  -Examples.

.EXAMPLE
  .\build.ps1
  .\build.ps1 -Web
  .\build.ps1 -All
  .\build.ps1 -Test
#>
[CmdletBinding()]
param(
    [switch]$All,
    [switch]$Web,
    [switch]$Examples,
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

# Get-Examples lists every program under examples\, so a new one is picked up
# here without this script having to name it.
function Get-Examples {
    Get-ChildItem -Directory examples -ErrorAction SilentlyContinue |
        Where-Object { Test-Path (Join-Path $_.FullName 'main.go') } |
        ForEach-Object { $_.Name }
}

function Build-One {
    param([string]$Goos, [string]$Goarch, [string]$Ext = '')
    $out = "dist/relay-tunnel-$Goos-$Goarch$Ext"
    Write-Host "  $out"
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags $ldflags -o $out $pkg
    if ($LASTEXITCODE -ne 0) { throw "build failed for $Goos/$Goarch" }

    # The examples ship next to the CLI so they can be run without a toolchain.
    # They carry no version stamp of their own, so only -s -w here.
    foreach ($name in Get-Examples) {
        $exOut = "dist/examples/$name-$Goos-$Goarch$Ext"
        Write-Host "  $exOut"
        go build -trimpath -ldflags '-s -w' -o $exOut "./examples/$name"
        if ($LASTEXITCODE -ne 0) { throw "build failed for example $name on $Goos/$Goarch" }
    }
}

# Build-ExamplesHost builds each example for this machine, next to the CLI.
function Build-ExamplesHost {
    $env:CGO_ENABLED = '0'
    try {
        foreach ($name in Get-Examples) {
            Write-Host "  $name.exe"
            go build -trimpath -ldflags '-s -w' -o "$name.exe" "./examples/$name"
            if ($LASTEXITCODE -ne 0) { throw "build failed for example $name" }
        }
    }
    finally { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue }
}

# Build-Web exports the frontend into the package that embeds it. npm ci is used
# when there is a lockfile to honour, which is the reproducible path CI wants.
function Build-Web {
    Write-Host 'Building the web interface'
    Push-Location web
    try {
        if (Test-Path 'package-lock.json') { npm ci --no-audit --no-fund }
        else { npm install --no-audit --no-fund }
        if ($LASTEXITCODE -ne 0) { throw 'npm install failed' }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw 'the web build failed' }
    }
    finally { Pop-Location }
}

if ($Help) {
    Write-Host 'Usage: .\build.ps1 [-All | -Web | -Examples | -Test | -Help]'
    Write-Host '  (no args)   build relay-tunnel.exe for this machine'
    Write-Host '  -Web        export the admin web interface into internal\webui\out'
    Write-Host '  -Examples   build everything under examples\ for this machine'
    Write-Host '  -All        build the web interface, then cross-compile the CLI and the examples into dist\'
    Write-Host '  -Test       gofmt, go vet and go test'
    return
}

if ($Web) {
    Build-Web
    return
}

if ($Examples) {
    Write-Host 'Building the examples for this machine'
    Build-ExamplesHost
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
    Build-Web
    Write-Host "Building relay-tunnel $version and the examples for all targets"
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    New-Item -ItemType Directory -Force -Path dist/examples | Out-Null
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
    # One checksums file over every artifact, examples included, which is the
    # same shape the release publishes. Names are relative to dist\.
    $sumsPath = Join-Path $PSScriptRoot 'dist/SHA256SUMS'
    if (Test-Path $sumsPath) { Remove-Item $sumsPath }
    $lines = Get-ChildItem dist -Recurse -File | Where-Object { $_.Name -ne 'SHA256SUMS' } | ForEach-Object {
        $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
        $rel = (Resolve-Path -Relative $_.FullName) -replace '^\.[\\/]dist[\\/]', '' -replace '\\', '/'
        "$h  $rel"
    }
    # Written by hand rather than with Out-File: Windows PowerShell writes a BOM
    # and CRLF, and `sha256sum -c` reads both as part of the line.
    [IO.File]::WriteAllText($sumsPath, ($lines -join "`n") + "`n", (New-Object Text.UTF8Encoding $false))
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
if (-not (Test-Path 'internal/webui/out/index.html')) {
    Write-Host '  note: no web interface embedded (run .\build.ps1 -Web first)'
}
