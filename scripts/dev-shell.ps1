# Build Trove from this checkout and make `trove` run it in the current PowerShell window.
# Use it by dot-sourcing, so the settings stay in your window:
#
#   . .\scripts\dev-shell.ps1
#   trove doctor
#
# It changes only this window: PATH gets the dev build first (so `trove` is this build, not any
# other program called trove), and TROVE_DATA_DIR points at a throwaway folder so your real
# ~/.trove is never opened. Close the window and everything is back as it was.
# Pass -DataDir to choose the folder, or set TROVE_DATA_DIR yourself first to keep your own.
param([string]$DataDir = "")

$repo = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $repo ".dev\bin"
New-Item -ItemType Directory -Force $bin | Out-Null

Push-Location $repo
try {
    go build -o (Join-Path $bin "trove.exe") ./cmd/trove
    if ($LASTEXITCODE -ne 0) { Write-Error "Build failed. Fix the error above, then run this again."; return }
} finally { Pop-Location }

$env:Path = "$bin;" + (($env:Path -split ';' | Where-Object { $_ -and $_ -ne $bin }) -join ';')
if ($DataDir) { $env:TROVE_DATA_DIR = $DataDir }
elseif (-not $env:TROVE_DATA_DIR) { $env:TROVE_DATA_DIR = Join-Path $env:TEMP "trove-dev" }

$found = (Get-Command trove -ErrorAction SilentlyContinue).Source
if ($found -ne (Join-Path $bin "trove.exe")) {
    Write-Warning "`trove` still resolves to $found. Do not run it; tell me what this prints."
    return
}
Write-Host "trove -> $found"
Write-Host "data  -> $env:TROVE_DATA_DIR (throwaway; your real ~/.trove is not used)"
