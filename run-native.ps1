# Dev launcher for the native shell (watchlive-native.exe).
# Builds the native binary into the project folder, copies mpv.exe next to it
# (from .\mpv\ — run .\fetch-mpv.ps1 once to populate that), and runs.
#
# The native shell starts the full server in-process on a loopback port and
# opens one WebView2 "remote" window. Video plays in external mpv.exe windows
# (one per screen tile) that the remote drives over mpv's IPC and auto-tiles.
#
# Usage: .\fetch-mpv.ps1     # once, to get mpv.exe
#        .\run-native.ps1
$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$exe  = Join-Path $root 'watchlive-native.exe'
$mpv  = Join-Path $root 'mpv.exe'
$src  = Join-Path $root 'mpv\mpv.exe'

if (-not (Test-Path $mpv)) {
  if (Test-Path $src) {
    Copy-Item $src $mpv -Force
  } else {
    Write-Warning "mpv.exe not found. Run .\fetch-mpv.ps1 first (or drop mpv.exe in the project root)."
    Write-Warning "Without it, the remote window opens but no player windows can start."
  }
}

Write-Host 'Building watchlive-native.exe...'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -o $exe ./cmd/watchlive-native
if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

& $exe @args
