# Build the shippable Windows bundle locally.
# Produces dist/watchlive-windows-amd64.zip — the same artifact the GitHub
# release workflow publishes. Run from the repo root: .\build.ps1
$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
$zip  = Join-Path $root 'dist\watchlive-windows-amd64.zip'

# Fresh dist/ each time so stale files don't end up in the zip.
if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
New-Item -ItemType Directory -Path $dist | Out-Null

Write-Host 'Building watchlive.exe...'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -ldflags "-s -w" -o (Join-Path $dist 'watchlive.exe') .
if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

Copy-Item (Join-Path $root 'WatchLive.bat') $dist
Copy-Item (Join-Path $root 'README.md') $dist

Write-Host 'Zipping bundle...'
Compress-Archive -Path (Join-Path $dist '*') -DestinationPath $zip -Force

Write-Host "Done: $zip"
