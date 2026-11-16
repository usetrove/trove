# Tests install.ps1 against a fake release served from 127.0.0.1. No access to GitHub is needed.
# Run: powershell -NoProfile -File install_test.ps1
#
# The installer adds its folder to PATH. These tests point it at the PROCESS PATH (TROVE_PATH_SCOPE=Process) so a developer's
# real PATH is never touched. The one test of the real USER PATH runs only on GitHub Actions, where the machine is thrown away.
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path $here 'install.ps1'
$work = Join-Path ([IO.Path]::GetTempPath()) ('trove-installtest-' + [Guid]::NewGuid().ToString('N'))
$port = 8791
$script:failures = 0
$originalProcessPath = $env:Path

function Check([string]$name, [bool]$ok) {
    if ($ok) { Write-Host "ok   - $name" } else { Write-Host "FAIL - $name"; $script:failures++ }
}

# Runs the installer the way users do (read it, pipe it to Invoke-Expression) and returns everything it printed.
function Run-Installer { (Get-Content -Raw $installer | Invoke-Expression *>&1 | Out-String) }

# How many PATH entries equal the install folder.
function Count-OnPath([string]$pathValue, [string]$folder) {
    @(($pathValue -split ';') | Where-Object { $_ -and ($_.TrimEnd('\') -ieq $folder.TrimEnd('\')) }).Count
}

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
    $env:TROVE_PATH_SCOPE = 'Process'
    Remove-Item Env:TROVE_REQUIRE_SIGNATURE, Env:TROVE_NO_PATH -ErrorAction SilentlyContinue

    # 1. a good checksum installs the binary
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value "$hash  $file"
    $out = Run-Installer
    Check 'good checksum installs trove.exe' (Test-Path (Join-Path $dest 'trove.exe'))
    Check 'the installed file is the one from the archive' ((Get-Content -Raw (Join-Path $dest 'trove.exe')) -eq 'fake-trove')
    Check 'says it installed, and that the signature was not checked' (($out -match 'Installed') -and ($out -match 'signature not checked'))

    # 2. it adds the folder to PATH by itself, says so, and the current window can run trove at once
    Check 'adds the install folder to PATH' ((Count-OnPath ([Environment]::GetEnvironmentVariable('Path', 'Process')) $dest) -eq 1)
    Check 'tells the user what it changed and how to undo it' (($out -match 'Added .* PATH') -and ($out -match 'To undo'))
    Check 'trove is runnable from PATH in the same window' ($null -ne (Get-Command trove -ErrorAction SilentlyContinue))

    # 3. installing again does not add the folder twice
    $null = Run-Installer
    Check 'a second install does not duplicate the PATH entry' ((Count-OnPath $env:Path $dest) -eq 1)

    # 4. TROVE_NO_PATH=1 leaves PATH alone
    Remove-Item -Recurse -Force $dest
    $env:Path = $originalProcessPath
    [Environment]::SetEnvironmentVariable('Path', $originalProcessPath, 'Process')
    $env:TROVE_NO_PATH = '1'
    $out = Run-Installer
    Check 'TROVE_NO_PATH=1 leaves PATH alone and says how to add it' (((Count-OnPath $env:Path $dest) -eq 0) -and ($out -match 'left alone'))
    Remove-Item Env:TROVE_NO_PATH

    # 5. a wrong checksum refuses and installs nothing
    Remove-Item -Recurse -Force $dest
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value ('0' * 64 + "  $file")
    $out = Run-Installer
    Check 'wrong checksum is refused' (($out -match 'checksum mismatch') -and -not (Test-Path (Join-Path $dest 'trove.exe')))

    # 6. an archive missing from checksums.txt refuses
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value ("$hash  some_other_file.zip")
    $out = Run-Installer
    Check 'no checksum entry is refused' (($out -match 'no entry for') -and -not (Test-Path (Join-Path $dest 'trove.exe')))

    # 7. requiring a signature without cosign refuses
    Set-Content -Path (Join-Path $rel 'checksums.txt') -Value "$hash  $file"
    $env:TROVE_REQUIRE_SIGNATURE = '1'
    $env:TROVE_COSIGN = 'trove-no-such-cosign'
    $out = Run-Installer
    Check 'TROVE_REQUIRE_SIGNATURE=1 without cosign is refused' (($out -match 'TROVE_REQUIRE_SIGNATURE=1') -and -not (Test-Path (Join-Path $dest 'trove.exe')))
    Remove-Item Env:TROVE_REQUIRE_SIGNATURE, Env:TROVE_COSIGN -ErrorAction SilentlyContinue

    # 8. an unsupported CPU refuses
    $env:TROVE_ARCH = 'riscv64'
    $out = Run-Installer
    Check 'unsupported CPU is refused' ($out -match 'unsupported CPU')
    $env:TROVE_ARCH = 'amd64'

    # 9. a version that does not exist refuses
    $env:TROVE_VERSION = '9.9.9'
    $out = Run-Installer
    Check 'a missing version is refused' (($out -match 'download failed') -and -not (Test-Path (Join-Path $dest 'trove.exe')))
    $env:TROVE_VERSION = '1.0.0'

    # 10. the real USER PATH, on a throwaway CI machine only: it is added, an existing %VARIABLE% entry survives, and it is restored
    if ($env:GITHUB_ACTIONS -eq 'true') {
        $key = Get-Item -Path 'HKCU:\Environment'
        $hadPath = $key.GetValueNames() -contains 'Path'
        $rawBefore = if ($hadPath) { $key.GetValue('Path', '', 'DoNotExpandEnvironmentNames') } else { $null }
        try {
            [Environment]::SetEnvironmentVariable('Path', '%USERPROFILE%\existing-tools', 'User')
            $env:TROVE_PATH_SCOPE = 'User'
            $env:Path = $originalProcessPath
            Remove-Item -Recurse -Force $dest -ErrorAction SilentlyContinue
            $out = Run-Installer
            $rawAfter = (Get-Item -Path 'HKCU:\Environment').GetValue('Path', '', 'DoNotExpandEnvironmentNames')
            Check 'user PATH gains the install folder' ((Count-OnPath $rawAfter $dest) -eq 1)
            Check 'an existing %VARIABLE% entry in the user PATH is kept as written' ($rawAfter -like '%USERPROFILE%\existing-tools;*')
        }
        finally {
            if ($hadPath) { [Environment]::SetEnvironmentVariable('Path', $rawBefore, 'User') } else { [Environment]::SetEnvironmentVariable('Path', $null, 'User') }
            $env:TROVE_PATH_SCOPE = 'Process'
        }
    }
}
finally {
    Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
    $env:Path = $originalProcessPath
    Remove-Item Env:TROVE_VERSION, Env:TROVE_ARCH, Env:TROVE_BASE_URL, Env:TROVE_INSTALL_DIR, Env:TROVE_PATH_SCOPE, Env:TROVE_NO_PATH, Env:TROVE_REQUIRE_SIGNATURE, Env:TROVE_COSIGN -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

if ($script:failures -gt 0) { Write-Host "$script:failures check(s) failed"; exit 1 }
Write-Host 'all install.ps1 checks passed'
