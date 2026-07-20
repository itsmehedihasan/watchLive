# Build the shippable Windows bundle locally.
# Produces dist/watchlive-windows-amd64.zip — the same artifact the GitHub
# release workflow publishes. Run from the repo root: .\build.ps1
$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
$zip  = Join-Path $root 'dist\watchlive-windows-amd64.zip'

# Fetch ffmpeg.exe into the embed folder so `go build` bakes it into the binary
# (single self-contained .exe with recording). Gitignored — not committed.
$ffmpegDir = Join-Path $root 'internal\ffmpeg\bin'
$ffmpegExe = Join-Path $ffmpegDir 'ffmpeg.exe'
New-Item -ItemType Directory -Path $ffmpegDir -Force | Out-Null
if (-not (Test-Path $ffmpegExe)) {
  Write-Host 'Fetching ffmpeg.exe (embedded for recording)...'
  $tmpZip = Join-Path $env:TEMP 'watchlive-ffmpeg.zip'
  $tmpDir = Join-Path $env:TEMP 'watchlive-ffmpeg'
  # gyan.dev "essentials" build — a static ffmpeg.exe, no install needed.
  $url = 'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip'
  Invoke-WebRequest -Uri $url -OutFile $tmpZip
  if (Test-Path $tmpDir) { Remove-Item -Recurse -Force $tmpDir }
  Expand-Archive -Path $tmpZip -DestinationPath $tmpDir -Force
  $found = Get-ChildItem -Path $tmpDir -Recurse -Filter 'ffmpeg.exe' | Select-Object -First 1
  if (-not $found) { throw 'ffmpeg.exe not found in downloaded archive' }
  Copy-Item $found.FullName $ffmpegExe -Force
  Remove-Item -Recurse -Force $tmpDir; Remove-Item -Force $tmpZip
  Write-Host "ffmpeg.exe ready ($([math]::Round((Get-Item $ffmpegExe).Length / 1MB)) MB)"
} else {
  Write-Host 'ffmpeg.exe already present, skipping download.'
}

# Fresh dist/ each time so stale files don't end up in the zip.
if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
New-Item -ItemType Directory -Path $dist | Out-Null

Write-Host 'Building watchlive.exe...'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -ldflags "-s -w" -o (Join-Path $dist 'watchlive.exe') ./cmd/watchlive
if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

# Native shell (WebView2 remote UI; video plays in external mpv.exe windows).
# Ships in the same bundle beside the web binary and needs mpv.exe next to it at
# runtime — staged below.
Write-Host 'Building watchlive-native.exe...'
go build -ldflags "-s -w" -o (Join-Path $dist 'watchlive-native.exe') ./cmd/watchlive-native
if ($LASTEXITCODE -ne 0) { throw "go build (native) failed ($LASTEXITCODE)" }

# Stage mpv.exe from .\mpv\ (fetch-mpv.ps1 populates it). Warn rather than fail so
# a web-only bundle can still be built without it.
$mpv = Join-Path $root 'mpv\mpv.exe'
if (Test-Path $mpv) {
  Copy-Item $mpv $dist
  Write-Host "Staged mpv.exe ($([math]::Round((Get-Item $mpv).Length / 1MB)) MB)"
} else {
  Write-Warning "mpv.exe not found (run .\fetch-mpv.ps1). watchlive-native.exe will not play video without it."
}

Copy-Item (Join-Path $root 'WatchLive.bat') $dist
Copy-Item (Join-Path $root 'README.md') $dist
Copy-Item (Join-Path $root 'THIRD-PARTY-NOTICES.md') $dist

Write-Host 'Zipping bundle...'
Compress-Archive -Path (Join-Path $dist '*') -DestinationPath $zip -Force

Write-Host "Done: $zip"
