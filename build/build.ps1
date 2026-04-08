$ErrorActionPreference = 'Stop'

$root_dir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$go_root = Join-Path $root_dir 'build_cache\go'
$go_bin = Join-Path $go_root 'bin\go.exe'

function Get-GoTool {
    if (Get-Command go -ErrorAction SilentlyContinue) {
        return (Get-Command go).Source
    }
    if (Test-Path $go_bin) {
        return $go_bin
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $root_dir 'build_cache') | Out-Null

    $version = '1.25.0'
    $arch = if ([Environment]::Is64BitOperatingSystem) { 'amd64' } else { '386' }
    $url = "https://go.dev/dl/go$version.windows-$arch.zip"
    $zip_path = Join-Path $root_dir "build_cache\go-$version.zip"

    Invoke-WebRequest -Uri $url -OutFile $zip_path
    if (Test-Path $go_root) {
        Remove-Item -Recurse -Force $go_root
    }
    Expand-Archive -Path $zip_path -DestinationPath (Join-Path $root_dir 'build_cache') -Force
    return $go_bin
}

$go = Get-GoTool
New-Item -ItemType Directory -Force -Path (Join-Path $root_dir 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $root_dir 'build_cache\gocache') | Out-Null

$env:GOCACHE = Join-Path $root_dir 'build_cache\gocache'
& $go build -o (Join-Path $root_dir 'bin\hx.exe') .\src

