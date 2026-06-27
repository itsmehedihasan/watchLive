# Dev launcher: build the exe INTO the project folder, then run it.
# Use this instead of `go run .` — `go run` compiles to a temp go-build dir,
# so the binary (and any exe-relative paths) live under %TEMP%, not here.
# Building locally keeps watchlive.exe and its state (store/, keys.json) in the
# project root. The exe is gitignored, so it won't be committed.
#
# Usage: .\run.ps1                 # build + run
#        .\run.ps1 -open=false     # pass any flags straight through to the exe
$ErrorActionPreference = 'Stop'

$root = $PSScriptRoot
$exe  = Join-Path $root 'watchlive.exe'

Write-Host 'Building watchlive.exe...'
go build -o $exe .
if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

& $exe @args
