@echo off
REM Double-click launcher for watchlive.
REM Runs the server from this folder so list.m3u is found next to the binary,
REM and the server opens your browser automatically once it's ready.
cd /d "%~dp0"
watchlive.exe
pause
