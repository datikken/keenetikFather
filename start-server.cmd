@echo off
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
    echo Go is not installed or is not available in PATH.
    echo Install Go 1.22 or newer and run this command again.
    pause
    exit /b 1
)

go run ./cmd/setup-server
set "exitCode=%ERRORLEVEL%"
if not "%exitCode%"=="0" pause
exit /b %exitCode%
