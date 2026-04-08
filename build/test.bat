@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0.."
call "%ROOT_DIR%\build.bat"
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%

set "GO_EXE=%ROOT_DIR%\build_cache\go_sdk\bin\go.exe"
if not exist "%GO_EXE%" (
    for /f "delims=" %%I in ('where go') do (
        set "GO_EXE=%%I"
        goto run_tests
    )
)

:run_tests
if not exist "%ROOT_DIR%\tests_cache\gocache" mkdir "%ROOT_DIR%\tests_cache\gocache"
set "GOCACHE=%ROOT_DIR%\tests_cache\gocache"
"%GO_EXE%" test ./tests/...
exit /b %ERRORLEVEL%
