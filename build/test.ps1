$ErrorActionPreference = 'Stop'

$root_dir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
& (Join-Path $root_dir 'build\build.ps1')

$go = if (Get-Command go -ErrorAction SilentlyContinue) {
    (Get-Command go).Source
} else {
    Join-Path $root_dir 'build_cache\go\bin\go.exe'
}

New-Item -ItemType Directory -Force -Path (Join-Path $root_dir 'tests_cache\gocache') | Out-Null
$env:GOCACHE = Join-Path $root_dir 'tests_cache\gocache'
& $go test ./tests/...

