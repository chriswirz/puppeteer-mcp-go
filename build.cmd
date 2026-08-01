@echo off
REM Convenience wrapper that runs build.ps1, so a double-click or a plain
REM `build` from cmd.exe works without thinking about the execution policy.
REM Run from this directory whichever directory the caller was in.
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "build.ps1" %*
exit /b %ERRORLEVEL%
