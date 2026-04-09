@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0.."
set "BUILD_CACHE=%ROOT_DIR%\build_cache"
set "GO_ROOT=%BUILD_CACHE%\go"
set "LEGACY_GO_ROOT=%BUILD_CACHE%\go_sdk"
set "GO_STAGE=%BUILD_CACHE%\go_stage"
set "GO_EXE=%GO_ROOT%\bin\go.exe"
set "GO_VERSION=1.26.2"
set "ARCH=amd64"

if /I "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "ARCH=arm64"

if exist "%GO_EXE%" (
    for /f "delims=" %%I in ('"%GO_EXE%" version') do set "GO_VERSION_TEXT=%%I %%J %%K"
    echo %GO_VERSION_TEXT% | findstr /C:"go%GO_VERSION%" >nul
    if %ERRORLEVEL%==0 goto build
)

if not exist "%BUILD_CACHE%" mkdir "%BUILD_CACHE%"
if exist "%GO_ROOT%" rmdir /s /q "%GO_ROOT%"
if exist "%LEGACY_GO_ROOT%" rmdir /s /q "%LEGACY_GO_ROOT%"
if exist "%GO_STAGE%" rmdir /s /q "%GO_STAGE%"
if exist "%GO_ROOT%" powershell -NoProfile -Command "Remove-Item -LiteralPath '%GO_ROOT%' -Recurse -Force"
if exist "%LEGACY_GO_ROOT%" powershell -NoProfile -Command "Remove-Item -LiteralPath '%LEGACY_GO_ROOT%' -Recurse -Force"
if exist "%GO_STAGE%" powershell -NoProfile -Command "Remove-Item -LiteralPath '%GO_STAGE%' -Recurse -Force"
mkdir "%GO_STAGE%"
set "GO_ARCHIVE=%BUILD_CACHE%\go-%GO_VERSION%.windows-%ARCH%.zip"
if exist "%GO_ARCHIVE%" del /f /q "%GO_ARCHIVE%"
curl -L "https://go.dev/dl/go%GO_VERSION%.windows-%ARCH%.zip" -o "%GO_ARCHIVE%"
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%
tar -xf "%GO_ARCHIVE%" -C "%GO_STAGE%"
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%
move "%GO_STAGE%\go" "%GO_ROOT%" >nul
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%
if not exist "%GO_EXE%" exit /b 1

:build
if not exist "%ROOT_DIR%\bin" mkdir "%ROOT_DIR%\bin"
if not exist "%BUILD_CACHE%\gocache" mkdir "%BUILD_CACHE%\gocache"
set "GOCACHE=%BUILD_CACHE%\gocache"
"%GO_EXE%" build -o "%ROOT_DIR%\bin\hx.exe" .\src
exit /b %ERRORLEVEL%
