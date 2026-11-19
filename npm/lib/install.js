'use strict';
// Downloads the release archive for this platform, verifies its SHA-256 against
// the release's checksums.txt, extracts it, and caches the binary.
// Never runs a binary whose checksum did not match.

const crypto = require('node:crypto');
const fs = require('node:fs');
const https = require('node:https');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const DEFAULT_BASE = 'https://github.com/usetrove/trove/releases/download';

function target(platform = process.platform, arch = process.arch) {
  const goos = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[platform];
  const goarch = { x64: 'amd64', arm64: 'arm64' }[arch];
  if (!goos || !goarch) {
    throw new Error(
      `Trove has no prebuilt binary for ${platform}/${arch}. Supported: macOS, Linux and Windows on x64 or arm64. ` +
      'Build from source instead: go install github.com/usetrove/trove/cmd/trove@latest');
  }
  return { goos, goarch };
}

// Windows releases are .zip files (Explorer opens them); the others are .tar.gz. Matches .goreleaser.yaml.
function archiveName(version, t) {
  return `trove_${version}_${t.goos}_${t.goarch}.${t.goos === 'windows' ? 'zip' : 'tar.gz'}`;
}

function binaryName(t) {
  return t.goos === 'windows' ? 'trove.exe' : 'trove';
}

// checksums.txt lines look like "<sha256>  <filename>".
function expectedChecksum(text, filename) {
  for (const line of text.split(/\r?\n/)) {
    const m = /^([0-9a-fA-F]{64})\s+\*?(\S+)$/.exec(line.trim());
    if (m && m[2] === filename) return m[1].toLowerCase();
  }
  throw new Error(`checksums.txt has no entry for ${filename}. The release may be incomplete; try again later or report it.`);
}

function verify(buf, checksumsText, filename) {
  const want = expectedChecksum(checksumsText, filename);
  const got = crypto.createHash('sha256').update(buf).digest('hex');
  if (got !== want) {
    throw new Error(`Checksum mismatch for ${filename} (expected ${want}, got ${got}). The download was corrupted or tampered with, so it was not run. Try again.`);
  }
}

function get(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith('https:') ? https : http;
    mod.get(url, { headers: { 'user-agent': 'trove-npm' } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        if (redirects <= 0) return reject(new Error(`Too many redirects fetching ${url}`));
        return resolve(get(new URL(res.headers.location, url).toString(), redirects - 1));
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`Download failed (HTTP ${res.statusCode}) for ${url}. Check your network and that this version exists.`));
      }
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve(Buffer.concat(chunks)));
      res.on('error', reject);
    }).on('error', (e) => reject(new Error(`Could not reach ${url}: ${e.message}. Check your network connection.`)));
  });
}

function cacheRoot() {
  if (process.platform === 'win32') {
    return process.env.LOCALAPPDATA || path.join(os.homedir(), 'AppData', 'Local');
  }
  return process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache');
}

// Windows 10 and later ship bsdtar as System32\tar.exe, which reads zip files. It is called by full path on purpose: a plain
// "tar" on PATH is often Git for Windows' GNU tar, which cannot read zip files or C:\ paths.
function windowsTar() {
  const p = path.join(process.env.SystemRoot || process.env.windir || 'C:\\Windows', 'System32', 'tar.exe');
  return fs.existsSync(p) ? p : null;
}

// Unpacks the one file we need. If System32\tar.exe is missing, PowerShell's Expand-Archive does the same job.
function extract(archivePath, dir, t) {
  const exe = binaryName(t);
  if (t.goos !== 'windows') {
    execFileSync('tar', ['-xzf', archivePath, '-C', dir, exe], { stdio: 'ignore' });
    return;
  }
  const tar = windowsTar();
  if (tar) {
    try {
      execFileSync(tar, ['-xf', archivePath, '-C', dir, exe], { stdio: 'ignore', windowsHide: true });
      return;
    } catch { /* fall through to PowerShell */ }
  }
  const q = (s) => `'${s.replace(/'/g, "''")}'`;
  execFileSync('powershell', ['-NoProfile', '-NonInteractive', '-Command',
    `Expand-Archive -LiteralPath ${q(archivePath)} -DestinationPath ${q(dir)} -Force`], { stdio: 'ignore', windowsHide: true });
}

// Returns the path to a verified trove binary, downloading it if not cached.
async function ensureBinary({ version, baseUrl = DEFAULT_BASE, cacheDir = path.join(cacheRoot(), 'trove-npm'), platform, arch } = {}) {
  if (!version || version === '0.0.0') {
    throw new Error('This @usetrove/trove package has no release version. Install a published version: npm install -g @usetrove/trove');
  }
  const t = target(platform, arch);
  const exe = binaryName(t);
  const bin = path.join(cacheDir, version, exe);
  if (fs.existsSync(bin)) return bin;

  const file = archiveName(version, t);
  const root = `${baseUrl}/v${version}`;
  const [archive, sums] = await Promise.all([get(`${root}/${file}`), get(`${root}/checksums.txt`)]);
  verify(archive, sums.toString('utf8'), file);

  const dir = path.join(cacheDir, version);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const tmp = fs.mkdtempSync(path.join(dir, 'x-'));
  try {
    const packed = path.join(tmp, file);
    fs.writeFileSync(packed, archive);
    extract(packed, tmp, t);
    if (!fs.existsSync(path.join(tmp, exe))) throw new Error(`${file} does not contain ${exe}. The release may be incomplete.`);
    fs.chmodSync(path.join(tmp, exe), 0o755);
    try {
      fs.renameSync(path.join(tmp, exe), bin);
    } catch (e) {
      // Another run may have cached the same version a moment ago; on Windows a rename onto an existing file fails.
      if (!fs.existsSync(bin)) throw e;
    }
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  return bin;
}

module.exports = { target, archiveName, binaryName, windowsTar, expectedChecksum, verify, ensureBinary, DEFAULT_BASE };
