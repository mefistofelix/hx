@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0.."
set "HX_EXE=%ROOT_DIR%\bin\hx.exe"
set "TESTS_CACHE=%ROOT_DIR%\tests_cache\cli"

if not exist "%HX_EXE%" (
    echo test failed: missing binary %HX_EXE%
    exit /b 1
)

if exist "%TESTS_CACHE%" rmdir /s /q "%TESTS_CACHE%"
mkdir "%TESTS_CACHE%" || exit /b 1

set "CASE_DIR=%TESTS_CACHE%\local"
mkdir "%CASE_DIR%\out" || exit /b 1
> "%CASE_DIR%\payload.txt" echo plain
"%HX_EXE%" -quiet -q 1 -symlinks 0 "%CASE_DIR%\payload.txt" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\payload.txt" (
    echo test failed: missing file %CASE_DIR%\out\payload.txt
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\local_repath"
mkdir "%CASE_DIR%\src\nested\deep" || exit /b 1
mkdir "%CASE_DIR%\src\other" || exit /b 1
mkdir "%CASE_DIR%\out" || exit /b 1
> "%CASE_DIR%\src\nested\deep\payload.txt" echo rewrite
> "%CASE_DIR%\src\other\keep.txt" echo skipme
"%HX_EXE%" -quiet "%CASE_DIR%\src" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\nested\deep\payload.txt" (
    echo test failed: missing file %CASE_DIR%\out\nested\deep\payload.txt
    exit /b 1
)
"%HX_EXE%" -quiet -repath "**/payload*" "%CASE_DIR%\src" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\payload.txt" (
    echo test failed: missing file %CASE_DIR%\out\payload.txt
    exit /b 1
)
if exist "%CASE_DIR%\out\other\keep.txt" (
    echo test failed: unexpected file %CASE_DIR%\out\other\keep.txt
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\link"
mkdir "%CASE_DIR%\out" || exit /b 1
> "%CASE_DIR%\payload.txt" echo linked
set "LINK_TEST_READY=1"
pushd "%CASE_DIR%" >nul || exit /b 1
cmd /c mklink "src_link.txt" "payload.txt" >nul 2>nul
if errorlevel 1 (
    set "LINK_TEST_READY="
)
popd >nul
if not defined LINK_TEST_READY (
    echo test note: skipping windows symlink case, missing symlink privilege
)
if defined LINK_TEST_READY (
    "%HX_EXE%" -quiet "%CASE_DIR%\src_link.txt" "%CASE_DIR%\out" || exit /b 1
    dir /al "%CASE_DIR%\out\src_link.txt" >nul 2>nul
    if errorlevel 1 (
        echo test failed: missing symlink %CASE_DIR%\out\src_link.txt
        exit /b 1
    )
    for /f "delims=" %%I in ('powershell -NoProfile -Command "(Get-Item -LiteralPath ''%CASE_DIR%\out\src_link.txt'').Target"') do set "LINK_TARGET=%%I"
    if /I not "%LINK_TARGET%"=="payload.txt" (
        echo test failed: unexpected symlink target %LINK_TARGET%
        exit /b 1
    )
)

set "CASE_DIR=%TESTS_CACHE%\http"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -delpathseg 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\fs.go" (
    echo test failed: missing file %CASE_DIR%\out\fs.go
    exit /b 1
)
"%HX_EXE%" -quiet -delpathseg 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "%CASE_DIR%\out" || exit /b 1

set "CASE_DIR=%TESTS_CACHE%\tar_xz"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -delpathseg 1 "https://raw.githubusercontent.com/glennrp/libpng-releases/master/libpng-1.6.34.tar.xz" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\README" (
    echo test failed: missing file %CASE_DIR%\out\README
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\zst"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "https://london.mirror.pkgbuild.com/core/os/x86_64/bash-5.3.9-1-x86_64.pkg.tar.zst" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\usr\bin\bash" (
    echo test failed: missing file %CASE_DIR%\out\usr\bin\bash
    exit /b 1
)
dir /b "%CASE_DIR%\out\usr\share\doc\bash\README" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\usr\share\doc\bash\README
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\github"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "https://github.com/go-git/go-billy" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\go.mod" (
    echo test failed: missing file %CASE_DIR%\out\go.mod
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\github_tree"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "https://github.com/go-git/go-billy/tree/master" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\go.mod" (
    echo test failed: missing file %CASE_DIR%\out\go.mod
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\github_release_zip"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "https://github.com/osquery/osquery/releases/download/5.22.1/osquery-5.22.1.windows_x86_64.zip" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\osquery-5.22.1.windows_x86_64\Program Files\osquery\osqueryd\osqueryd.exe" (
    echo test failed: missing file %CASE_DIR%\out\osquery-5.22.1.windows_x86_64\Program Files\osquery\osqueryd\osqueryd.exe
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\pypi"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -delpathseg 1 "pypi://requests@2.32.3" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\pyproject.toml" (
    echo test failed: missing file %CASE_DIR%\out\pyproject.toml
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\nuget"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "nuget://Newtonsoft.Json@13.0.3" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\lib\net45\Newtonsoft.Json.dll" (
    echo test failed: missing file %CASE_DIR%\out\lib\net45\Newtonsoft.Json.dll
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\winget"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -platform "windows/amd64" "winget://Git.Git@2.46.0" "%CASE_DIR%\out" || exit /b 1
dir /b "%CASE_DIR%\out\Git-*-64-bit.exe" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\Git-*-64-bit.exe
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\npm"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet "npm://lodash@4.17.21" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\package\package.json" (
    echo test failed: missing file %CASE_DIR%\out\package\package.json
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\docker"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -platform "linux/amd64" "docker://busybox:1.36.1" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\bin\busybox" (
    echo test failed: missing file %CASE_DIR%\out\bin\busybox
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\docker_do"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -download-only 1 -platform "linux/amd64" "docker://busybox:1.36.1" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\manifest.json" (
    echo test failed: missing file %CASE_DIR%\out\manifest.json
    exit /b 1
)
dir /b "%CASE_DIR%\out\sha256-*.tar*" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\sha256-*.tar*
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\apk"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -target "v3.22/main" -platform "linux/amd64" "apk://curl" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\usr\bin\curl" (
    echo test failed: missing file %CASE_DIR%\out\usr\bin\curl
    exit /b 1
)
dir /b "%CASE_DIR%\out\usr\lib\libcurl.so.4*" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\usr\lib\libcurl.so.4*
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\apt"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -registry "https://deb.debian.org/debian" -target "bookworm/main" -platform "linux/amd64" "apt://curl" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\usr\bin\curl" (
    echo test failed: missing file %CASE_DIR%\out\usr\bin\curl
    exit /b 1
)
dir /b "%CASE_DIR%\out\usr\lib\x86_64-linux-gnu\libcurl.so.4*" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\usr\lib\x86_64-linux-gnu\libcurl.so.4*
    exit /b 1
)

set "CASE_DIR=%TESTS_CACHE%\rpm"
mkdir "%CASE_DIR%\out" || exit /b 1
"%HX_EXE%" -quiet -registry "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases" -target "41/Everything" -platform "linux/amd64" "rpm://jq" "%CASE_DIR%\out" || exit /b 1
if not exist "%CASE_DIR%\out\usr\bin\jq" (
    echo test failed: missing file %CASE_DIR%\out\usr\bin\jq
    exit /b 1
)
dir /b "%CASE_DIR%\out\usr\lib64\libonig.so.5*" >nul 2>nul
if errorlevel 1 (
    echo test failed: missing match %CASE_DIR%\out\usr\lib64\libonig.so.5*
    exit /b 1
)

echo cli smoke tests passed
exit /b 0
