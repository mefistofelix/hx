@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0.."
call "%ROOT_DIR%\build.bat"
if %ERRORLEVEL% neq 0 exit /b %ERRORLEVEL%
call "%ROOT_DIR%\tests\cli_smoke.bat"
exit /b %ERRORLEVEL%
