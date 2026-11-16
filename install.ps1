# Trove installer for Windows: downloads a release archive, verifies its SHA-256 against the release's
# checksums.txt, and installs trove.exe. It refuses to install on a mismatch.
#
#   irm https://raw.githubusercontent.com/usetrove/trove/main/install.ps1 | iex
#
# Environment (all optional):
#   TROVE_VERSION      version to install, e.g. 0.4.0 (default: latest release)
#   TROVE_INSTALL_DIR  where to put trove.exe (default: %LOCALAPPDATA%\Programs\trove)
#   TROVE_BASE_URL     release download base (default: GitHub Releases); for testing
#   TROVE_COSIGN       cosign command to use (default: cosign); for testing
#   TROVE_REQUIRE_SIGNATURE  set to 1 to refuse to install unless cosign verifies the release signature
#   TROVE_ARCH         override CPU detection (amd64|arm64); for testing
#
# Like install.sh, this never edits your PATH or any shell profile: it prints the one command that would.

function Install-Trove {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'   # the progress bar makes downloads far slower in Windows PowerShell

    $repo = 'usetrove/trove'
    $base = if ($env:TROVE_BASE_URL) { $env:TROVE_BASE_URL.TrimEnd('/') } else { "https://github.com/$repo/releases/download" }
    $dest = if ($env:TROVE_INSTALL_DIR) { $env:TROVE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\trove' }

    function Fail([string]$message) { throw "trove install: $message" }

    # Windows PowerShell 5.1 does not offer TLS 1.2 by default, and GitHub requires it.
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

    $cpu = if ($env:TROVE_ARCH) { $env:TROVE_ARCH } else { $env:PROCESSOR_ARCHITECTURE }
    # A 32-bit PowerShell on a 64-bit machine reports x86 here, and the real architecture is in PROCESSOR_ARCHITEW6432.
    if (-not $env:TROVE_ARCH -and $env:PROCESSOR_ARCHITEW6432) { $cpu = $env:PROCESSOR_ARCHITEW6432 }
    switch -Regex ($cpu) {
        '^(AMD64|x86_64|amd64)$' { $arch = 'amd64' }
        '^(ARM64|arm64|aarch64)$' { $arch = 'arm64' }
        default { Fail "unsupported CPU '$cpu'. Supported: x64 and ARM64. Build from source: go install github.com/$repo/cmd/trove@latest" }
    }

    $version = $env:TROVE_VERSION
    if (-not $version) {
        # The /releases/latest page redirects to /releases/tag/vX.Y.Z; read that without the API.
        try {
            $r = Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest" -UseBasicParsing -Method Head
            $final = if ($r.BaseResponse.ResponseUri) { $r.BaseResponse.ResponseUri.AbsoluteUri } else { $r.BaseResponse.RequestMessage.RequestUri.AbsoluteUri }
        } catch { Fail "could not look up the latest release. Check your network, or set `$env:TROVE_VERSION." }
        $version = ($final -split '/v')[-1]
    }
    $version = $version.TrimStart('v')
    if (-not $version) { Fail 'could not work out the version to install. Set $env:TROVE_VERSION.' }

    $file = "trove_${version}_windows_${arch}.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("trove-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading Trove $version for windows/$arch..."
        try { Invoke-WebRequest -Uri "$base/v$version/$file" -OutFile (Join-Path $tmp $file) -UseBasicParsing }
        catch { Fail "download failed for $base/v$version/$file. Check your network and that version $version exists." }
        try { Invoke-WebRequest -Uri "$base/v$version/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing }
        catch { Fail "could not download checksums.txt for version $version." }

        # The checksum file is signed (keyless cosign, by this repository's release workflow). A checksum from the same
        # place as the archive only catches corruption; the signature is what catches a tampered release. If cosign is
        # installed it is checked and a bad signature stops the install. Without cosign the install goes ahead on the
        # checksum alone and says so, unless TROVE_REQUIRE_SIGNATURE=1, which refuses to install unverified.
        $signature = 'checksums verified, signature not checked'
        $haveBundle = $false
        try { Invoke-WebRequest -Uri "$base/v$version/checksums.txt.sigstore.json" -OutFile (Join-Path $tmp 'checksums.txt.sigstore.json') -UseBasicParsing; $haveBundle = $true } catch { }
        $cosign = if ($env:TROVE_COSIGN) { $env:TROVE_COSIGN } else { 'cosign' }
        if ((Get-Command $cosign -ErrorAction SilentlyContinue) -and $haveBundle) {
            & $cosign verify-blob `
                --certificate-identity "https://github.com/$repo/.github/workflows/release.yml@refs/tags/v$version" `
                --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' `
                --bundle (Join-Path $tmp 'checksums.txt.sigstore.json') (Join-Path $tmp 'checksums.txt') *> $null
            if ($LASTEXITCODE -ne 0) { Fail "the signature on checksums.txt for version $version did not verify, so nothing was installed. The release may have been tampered with; report it at https://github.com/$repo/security" }
            $signature = 'checksum and signature verified'
        }
        elseif ($env:TROVE_REQUIRE_SIGNATURE -eq '1') {
            Fail 'TROVE_REQUIRE_SIGNATURE=1 is set but the release signature cannot be checked (cosign is not installed, or the release has no signature file), so nothing was installed. Install cosign and run this again.'
        }
        else {
            Write-Host 'Note: cosign is not installed, so the release signature was not checked, only the checksum. Install cosign and run this again to check it, or set $env:TROVE_REQUIRE_SIGNATURE=1 to refuse unverified installs.'
        }

        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and ($parts[1].TrimStart('*') -eq $file)) { $want = $parts[0]; break }
        }
        if (-not $want) { Fail "checksums.txt has no entry for $file. The release may be incomplete." }
        $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $file)).Hash
        if ($got.ToLower() -ne $want.ToLower()) {
            Fail "checksum mismatch for $file (expected $want, got $got). The download was corrupted or tampered with, so nothing was installed."
        }

        try { Expand-Archive -Path (Join-Path $tmp $file) -DestinationPath (Join-Path $tmp 'unpacked') -Force }
        catch { Fail "could not unpack $file." }
        $exe = Join-Path $tmp 'unpacked\trove.exe'
        if (-not (Test-Path $exe)) { Fail "$file does not contain trove.exe. The release may be incomplete." }
        try {
            New-Item -ItemType Directory -Path $dest -Force | Out-Null
            Copy-Item -Path $exe -Destination (Join-Path $dest 'trove.exe') -Force
        } catch { Fail "could not write $dest\trove.exe. Set `$env:TROVE_INSTALL_DIR to a writable folder, and close any running trove first." }
    }
    finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    Write-Host "Installed $dest\trove.exe ($signature)."
    $onPath = ($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $dest.TrimEnd('\') }
    if (-not $onPath) {
        Write-Host "Add it to your PATH to run 'trove' from anywhere (open a new terminal afterwards):"
        Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ';$dest', 'User')"
    }
}

try { Install-Trove } catch { Write-Host $_.Exception.Message -ForegroundColor Red }
