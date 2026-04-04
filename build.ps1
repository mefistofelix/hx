#!/usr/bin/env pwsh
# build.ps1 — self-contained build script for hx
# Downloads the Go toolchain into .\build\go\ on first run,
# then compiles hx for Windows + Linux into .\bin\.
param()
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# ── Configuration ──────────────────────────────────────────────────────────────
$GoVersion = '1.26.1'
$BuildDir  = Join-Path $PSScriptRoot 'build'
$GoRoot    = Join-Path $BuildDir 'go'
$GoExe     = Join-Path $GoRoot 'bin\go.exe'
$Src       = Join-Path $PSScriptRoot 'src'
$BinDir    = Join-Path $PSScriptRoot 'bin'

$env:GOROOT      = $GoRoot
$env:GOPATH      = Join-Path $BuildDir '.gopath'
$env:GOCACHE     = Join-Path $BuildDir '.gocache'
$env:CGO_ENABLED = '0'
$env:GOFLAGS     = ''

$null = New-Item -ItemType Directory -Force $BinDir | Out-Null

# ── Download + unpack Go toolchain if not already present ──────────────────────
if (-not (Test-Path $GoExe)) {
    $zip = "go$GoVersion.windows-amd64.zip"
    $url = "https://go.dev/dl/$zip"
    Write-Host "[1/3] Downloading Go $GoVersion ..."
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
    Write-Host "[2/3] Extracting Go toolchain to .\build\go\ ..."
    $null = New-Item -ItemType Directory -Force $BuildDir
    Expand-Archive -LiteralPath $zip -DestinationPath $BuildDir -Force
    Remove-Item $zip
}

# ── Fetch module dependencies on first build ───────────────────────────────────
if (-not (Test-Path (Join-Path $Src 'go.sum'))) {
    Write-Host "[2/3] Fetching dependencies ..."
    & $GoExe -C $Src get github.com/mholt/archives@latest
    if ($LASTEXITCODE -ne 0) { throw 'go get failed' }
    & $GoExe -C $Src mod tidy
    if ($LASTEXITCODE -ne 0) { throw 'go mod tidy failed' }
}

# ── Build ──────────────────────────────────────────────────────────────────────
Write-Host "[3/3] Building ..."
$env:GOARCH = 'amd64'

$env:GOOS = 'windows'
& $GoExe -C $Src build -ldflags '-s -w' -o (Join-Path $BinDir 'hx.exe') .
if ($LASTEXITCODE -ne 0) { throw 'build failed (windows/amd64)' }
Write-Host "  OK -> bin\hx.exe  (windows/amd64)"

$env:GOOS = 'linux'
& $GoExe -C $Src build -ldflags '-s -w' -o (Join-Path $BinDir 'hx') .
if ($LASTEXITCODE -ne 0) { throw 'build failed (linux/amd64)' }
Write-Host "  OK -> bin/hx      (linux/amd64)"
