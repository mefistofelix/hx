@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0"
if "%ROOT_DIR:~-1%"=="\" set "ROOT_DIR=%ROOT_DIR:~0,-1%"
call "%ROOT_DIR%\build\build.bat"
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%
call "%ROOT_DIR%\tests\cli_smoke.bat"
exit /b %ERRORLEVEL%
