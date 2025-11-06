@echo off
rem Thin wrapper that runs build.ps1 with any arguments passed through.
rem Examples:
rem   build.cmd            build relay-tunnel.exe for this machine
rem   build.cmd --all      cross-compile every release target into dist\
rem   build.cmd --test     gofmt, go vet and go test
setlocal

set "PS_ARGS="
:parse
if "%~1"=="" goto run
if /i "%~1"=="--all"  set "PS_ARGS=%PS_ARGS% -All"  & shift & goto parse
if /i "%~1"=="--test" set "PS_ARGS=%PS_ARGS% -Test" & shift & goto parse
if /i "%~1"=="--help" set "PS_ARGS=%PS_ARGS% -Help" & shift & goto parse
if /i "%~1"=="-h"     set "PS_ARGS=%PS_ARGS% -Help" & shift & goto parse
rem Pass through anything else verbatim (e.g. -All / -Test directly).
set "PS_ARGS=%PS_ARGS% %~1" & shift & goto parse

:run
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1"%PS_ARGS%
exit /b %ERRORLEVEL%
