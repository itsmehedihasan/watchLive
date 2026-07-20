# Fetch mpv.exe (the player) for the native shell (watchlive-native.exe).
#
# The native shell drives external mpv.exe player windows over mpv's JSON IPC.
# That player is ~40-80 MB and is NOT committed - this script downloads the
# latest shinchiro / mpv-player-windows player build and extracts mpv.exe into
# .\mpv\ (gitignored). run-native.ps1 and build.ps1 copy it next to the exe.
# Requires 7-Zip (the builds are .7z); if it is not installed, the script prints
# manual instructions.
#
# Usage: .\fetch-mpv.ps1
$ErrorActionPreference = 'Stop'

$root   = $PSScriptRoot
$outDir = Join-Path $root 'mpv'
$outExe = Join-Path $outDir 'mpv.exe'

if (Test-Path $outExe) {
  Write-Host "mpv.exe already present at $outExe - delete it to re-fetch."
  exit 0
}
New-Item -ItemType Directory -Path $outDir -Force | Out-Null

# Locate a 7-Zip CLI (the mpv builds are .7z; Windows has no native 7z).
$seven = $null
foreach ($c in @('7z', 'C:\Program Files\7-Zip\7z.exe', 'C:\Program Files (x86)\7-Zip\7z.exe')) {
  $cmd = Get-Command $c -ErrorAction SilentlyContinue
  if ($cmd) { $seven = $cmd.Source; break }
  if (Test-Path $c) { $seven = $c; break }
}
if (-not $seven) {
  Write-Warning "7-Zip not found. Install it (https://www.7-zip.org/) and re-run, OR manually:"
  Write-Host "  1) Download the latest 'mpv-x86_64-*.7z' (NOT mpv-dev, NOT -v3) from" -ForegroundColor Yellow
  Write-Host "     https://sourceforge.net/projects/mpv-player-windows/files/64bit/" -ForegroundColor Yellow
  Write-Host "  2) Extract mpv.exe into: $outDir" -ForegroundColor Yellow
  exit 1
}

# PS 5.1's Invoke-WebRequest defaults to old TLS; SourceForge mirrors need 1.2.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# The SourceForge RSS lists files newest-first; grab the newest baseline
# mpv-x86_64 player build (exclude the -v3 variant, which needs a newer CPU).
Write-Host 'Finding the latest mpv player build...'
$rss = Invoke-RestMethod -Uri 'https://sourceforge.net/projects/mpv-player-windows/rss?path=/64bit'
$item = $rss | Where-Object { $_.link -match 'mpv-x86_64-\d' -and $_.link -notmatch 'v3' } | Select-Object -First 1
if (-not $item) { throw 'Could not find an mpv-x86_64 build in the SourceForge feed.' }
# The RSS link is SourceForge's '/download' interstitial - an HTML page, not the
# file. Scripted clients don't get the mirror redirect a browser does, so we
# fetch that page and scrape the signed direct URL it embeds (data-release-url,
# carrying a fresh single-use 'ts' token), then download that.
$pageUrl = $item.link
Write-Host "Resolving mirror from: $pageUrl"
$page = Invoke-WebRequest -Uri $pageUrl -UseBasicParsing -Headers @{ 'User-Agent' = 'Mozilla/5.0' }
$m = [regex]::Match($page.Content, 'data-release-url="([^"]+)"')
if (-not $m.Success) {
  $m = [regex]::Match($page.Content, 'http-equiv="refresh"[^>]*url=([^"''&][^"'' >]*\.7z[^"'' >]*)')
}
if (-not $m.Success) { throw "Could not find the direct download URL in SourceForge's interstitial page." }
$url = ([System.Net.WebUtility]::HtmlDecode($m.Groups[1].Value)).Trim()
Write-Host "Downloading: $url"

$tmp7z = Join-Path $env:TEMP 'watchlive-mpv.7z'
$wc = New-Object Net.WebClient
$wc.Headers.Add('User-Agent', 'Mozilla/5.0')
try   { $wc.DownloadFile($url, $tmp7z) }
finally { $wc.Dispose() }

# Verify we got a real 7z archive (magic bytes 37 7A BC AF 27 1C), not an
# HTML error/redirect page saved with a .7z name. Keep the file on failure.
$magic = [byte[]](Get-Content -Path $tmp7z -Encoding Byte -TotalCount 6 -ErrorAction Stop)
$expected = 0x37,0x7A,0xBC,0xAF,0x27,0x1C
if (@(Compare-Object $magic $expected -SyncWindow 0).Count -ne 0) {
  throw "Downloaded file is not a 7z archive (got a redirect/HTML page?). Left it at $tmp7z for inspection."
}

Write-Host 'Extracting mpv.exe...'
& $seven e $tmp7z "-o$outDir" 'mpv.exe' '-y' | Out-Null
if (-not (Test-Path $outExe)) { throw "mpv.exe not found in the archive. Archive left at $tmp7z." }
Remove-Item -Force $tmp7z
Write-Host "Done: $outExe ($([math]::Round((Get-Item $outExe).Length / 1MB)) MB)" -ForegroundColor Green
