$ErrorActionPreference = "Stop"

$project_root = Split-Path -Parent $PSScriptRoot
$build_cache = Join-Path $project_root "build_cache"
$go_root = Join-Path $build_cache "go"
$go_bin = Join-Path $go_root "bin\go.exe"
$go_version = "1.22.3"
$go_archive = Join-Path $build_cache "go-download.zip"
$go_url = "https://go.dev/dl/go$go_version.windows-amd64.zip"

New-Item -ItemType Directory -Force -Path $build_cache | Out-Null

if (-not (Test-Path $go_bin)) {
    if (Test-Path $go_archive) {
        Remove-Item -Force $go_archive
    }
    Invoke-WebRequest -Uri $go_url -OutFile $go_archive
    if (Test-Path $go_root) {
        Remove-Item -Recurse -Force $go_root
    }
    Expand-Archive -Path $go_archive -DestinationPath $build_cache -Force
}

New-Item -ItemType Directory -Force -Path (Join-Path $project_root "bin") | Out-Null
Push-Location $project_root
try {
    & $go_bin test ./src
    & $go_bin build -o (Join-Path $project_root "bin\hx.exe") ./src
} finally {
    Pop-Location
}
