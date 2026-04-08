$ErrorActionPreference = "Stop"

$project_root = Split-Path -Parent $PSScriptRoot
$build_script = Join-Path $project_root "build\build.ps1"

& $build_script
