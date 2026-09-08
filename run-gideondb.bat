@echo off
setlocal

cd /d "%~dp0"

if not defined GIDEONDB_DASHBOARD_USERNAME set "GIDEONDB_DASHBOARD_USERNAME=admin"
if not defined GIDEONDB_DASHBOARD_PASSWORD set "GIDEONDB_DASHBOARD_PASSWORD=admin123"
if not defined GIDEONDB_DASHBOARD_BOOTSTRAP set "GIDEONDB_DASHBOARD_BOOTSTRAP=true"

go run ./cmd/gideondb %*
set "exit_code=%ERRORLEVEL%"

endlocal & exit /b %exit_code%
