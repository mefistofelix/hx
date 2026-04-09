@echo off
setlocal EnableExtensions EnableDelayedExpansion

set "ROOT_DIR=%~dp0.."
set "HX_EXE=%ROOT_DIR%\bin\hx.exe"
set "TESTS_CACHE=%ROOT_DIR%\tests_cache\cli"

if not exist "%HX_EXE%" (
    echo test failed: missing binary %HX_EXE%
    exit /b 1
)

if exist "%TESTS_CACHE%" rmdir /s /q "%TESTS_CACHE%"
mkdir "%TESTS_CACHE%" || exit /b 1

call :local_copy || exit /b 1
call :http_archive || exit /b 1
call :github_repo || exit /b 1
call :pypi || exit /b 1
call :nuget || exit /b 1
call :winget || exit /b 1
call :npm || exit /b 1
call :docker_extract || exit /b 1
call :docker_download_only || exit /b 1
call :apk || exit /b 1
call :apt || exit /b 1
call :rpm || exit /b 1

echo cli smoke tests passed
exit /b 0

:run_hx
"%HX_EXE%" -quiet %*
exit /b %ERRORLEVEL%

:require_file
if not exist "%~1" (
    echo test failed: missing file %~1
    exit /b 1
)
exit /b 0

:require_glob
dir /b %~1 >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %~1
    exit /b 1
)
exit /b 0

:local_copy
set "CASE_DIR=%TESTS_CACHE%\local"
mkdir "%CASE_DIR%\out" || exit /b 1
> "%CASE_DIR%\payload.txt" echo plain
call :run_hx -q 1 -symlinks 0 "%CASE_DIR%\payload.txt" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\payload.txt" || exit /b 1
exit /b 0

:http_archive
set "CASE_DIR=%TESTS_CACHE%\http"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -strip 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\file.go" || exit /b 1
call :run_hx -strip 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "%CASE_DIR%\out" || exit /b 1
exit /b 0

:github_repo
set "CASE_DIR=%TESTS_CACHE%\github"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx "https://github.com/go-git/go-billy" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\go.mod" || exit /b 1
exit /b 0

:pypi
set "CASE_DIR=%TESTS_CACHE%\pypi"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -strip 1 "pypi://requests@2.32.3" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\pyproject.toml" || exit /b 1
exit /b 0

:nuget
set "CASE_DIR=%TESTS_CACHE%\nuget"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx "nuget://Newtonsoft.Json@13.0.3" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\lib\net45\Newtonsoft.Json.dll" || exit /b 1
exit /b 0

:winget
set "CASE_DIR=%TESTS_CACHE%\winget"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -platform "windows/amd64" "winget://Git.Git@2.46.0" "%CASE_DIR%\out" || exit /b 1
call :require_glob "%CASE_DIR%\out\Git-*-64-bit.exe" || exit /b 1
exit /b 0

:npm
set "CASE_DIR=%TESTS_CACHE%\npm"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx "npm://lodash@4.17.21" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\package\package.json" || exit /b 1
exit /b 0

:docker_extract
set "CASE_DIR=%TESTS_CACHE%\docker"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -platform "linux/amd64" "docker://busybox:1.36.1" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\bin\busybox" || exit /b 1
exit /b 0

:docker_download_only
set "CASE_DIR=%TESTS_CACHE%\docker_do"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -download-only 1 -platform "linux/amd64" "docker://busybox:1.36.1" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\manifest.json" || exit /b 1
call :require_glob "%CASE_DIR%\out\sha256-*.tar*" || exit /b 1
exit /b 0

:apk
set "CASE_DIR=%TESTS_CACHE%\apk"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -target "v3.22/main" -platform "linux/amd64" "apk://curl" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\usr\bin\curl" || exit /b 1
call :require_glob "%CASE_DIR%\out\usr\lib\libcurl.so.4*" || exit /b 1
exit /b 0

:apt
set "CASE_DIR=%TESTS_CACHE%\apt"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -registry "https://deb.debian.org/debian" -target "bookworm/main" -platform "linux/amd64" "apt://curl" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\usr\bin\curl" || exit /b 1
call :require_glob "%CASE_DIR%\out\usr\lib\x86_64-linux-gnu\libcurl.so.4*" || exit /b 1
exit /b 0

:rpm
set "CASE_DIR=%TESTS_CACHE%\rpm"
mkdir "%CASE_DIR%\out" || exit /b 1
call :run_hx -registry "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases" -target "41/Everything" -platform "linux/amd64" "rpm://jq" "%CASE_DIR%\out" || exit /b 1
call :require_file "%CASE_DIR%\out\usr\bin\jq" || exit /b 1
call :require_glob "%CASE_DIR%\out\usr\lib64\libonig.so.5*" || exit /b 1
exit /b 0
