# Tests install.ps1 against a fake release served from 127.0.0.1. No access to GitHub is needed.
# Run: powershell -NoProfile -File install_test.ps1
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path $here 'install.ps1'
$work = Join-Path ([IO.Path]::GetTempPath()) ('trove-installtest-' + [Guid]::NewGuid().ToString('N'))
$port = 8791
$script:failures = 0

function Check([string]$name, [bool]$ok) {
    if ($ok) { Write-Host "ok   - $name" } else { Write-Host "FAIL - $name"; $script:failures++ }
}

# Runs the installer the way users do (read it, pipe it to Invoke-Expression) and returns everything it printed.
function Run-Installer { (Get-Content -Raw $installer | Invoke-Expression *>&1 | Out-String) }

$rel = Join-Path $work 'rel\v1.0.0'
$src = Join-Path $work 'src'
$dest = Join-Path $work 'dest'
New-Item -ItemType Directory -Path $rel, $src | Out-Null
Set-Content -Path (Join-Path $src 'trove.exe') -Value 'fake-trove' -NoNewline
$file = 'trove_1.0.0_windows_amd64.zip'
Compress-Archive -Path (Join-Path $src 'trove.exe') -DestinationPath (Join-Path $rel $file)
$hash = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $rel $file)).Hash.ToLower()

$server = Start-Process -FilePath python -ArgumentList '-m', 'http.server', $port, '--bind', '127.0.0.1', '--directory', (Join-Path $work 'rel') -PassThru -WindowStyle Hidden
try {
    Start-Sleep -Seconds 2
    $env:TROVE_VERSION = '1.0.0'
    $env:TROVE_ARCH = 'amd64'
    $env:TROVE_BASE_URL = "http://127.0.0.1:$port"
    $env:TROVE_INSTALL_DIR = $dest
    Remove-Item Env:TROVE_REQUIRE_SIGNATURE -ErrorAction SilentlyContinue

    # 1. a good checksum installs the binary
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value "$hash  $file"
    $out = Run-Installer
    Check 'good checksum installs trove.exe' (Test-Path (Join-Path $dest 'trove.exe'))
    Check 'the installed file is the one from the archive' ((Get-Content -Raw (Join-Path $dest 'trove.exe')) -eq 'fake-trove')
    Check 'says it installed, and that the signature was not checked' (($out -match 'Installed') -and ($out -match 'signature not checked'))
    Check 'prints the PATH command, and does not edit PATH' (($out -match 'Add it to your PATH') -and ([Environment]::GetEnvironmentVariable('Path', 'User') -notlike "*$dest*"))

    # 2. a wrong checksum refuses and installs nothing
    Remove-Item -Recurse -Force $dest
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value ('0' * 64 + "  $file")
    $out = Run-Installer
    Check 'wrong checksum is refused' (($out -match 'checksum mismatch') -and -not (Test-Path (Join-Path $dest 'trove.exe')))

    # 3. an archive missing from checksums.txt refuses
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value ("$hash  some_other_file.zip")
    $out = Run-Installer
    Check 'no checksum entry is refused' (($out -match 'no entry for') -and -not (Test-Path (Join-Path $dest 'trove.exe')))

    # 4. requiring a signature without cosign refuses
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value "$hash  $file"
    $env:TROVE_REQUIRE_SIGNATURE = '1'
    $env:TROVE_COSIGN = 'trove-no-such-cosign'
    $out = Run-Installer
    Check 'TROVE_REQUIRE_SIGNATURE=1 without cosign is refused' (($out -match 'TROVE_REQUIRE_SIGNATURE=1') -and -not (Test-Path (Join-Path $dest 'trove.exe')))
    Remove-Item Env:TROVE_REQUIRE_SIGNATURE, Env:TROVE_COSIGN -ErrorAction SilentlyContinue

    # 5. an unsupported CPU refuses
    $env:TROVE_ARCH = 'riscv64'
    $out = Run-Installer
    Check 'unsupported CPU is refused' ($out -match 'unsupported CPU')
    $env:TROVE_ARCH = 'amd64'

    # 6. a version that does not exist refuses
    $env:TROVE_VERSION = '9.9.9'
    $out = Run-Installer
    Check 'a missing version is refused' (($out -match 'download failed') -and -not (Test-Path (Join-Path $dest 'trove.exe')))
}
finally {
    Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
    Remove-Item Env:TROVE_VERSION, Env:TROVE_ARCH, Env:TROVE_BASE_URL, Env:TROVE_INSTALL_DIR, Env:TROVE_REQUIRE_SIGNATURE, Env:TROVE_COSIGN -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

if ($script:failures -gt 0) { Write-Host "$script:failures check(s) failed"; exit 1 }
Write-Host 'all install.ps1 checks passed'
