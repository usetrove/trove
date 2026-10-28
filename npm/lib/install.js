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
  const goos = { darwin: 'darwin', linux: 'linux' }[platform];
  const goarch = { x64: 'amd64', arm64: 'arm64' }[arch];
  if (!goos || !goarch) {
    throw new Error(
      `Trove has no prebuilt binary for ${platform}/${arch}. Supported: macOS and Linux on x64 or arm64. ` +
      'Build from source instead: go install github.com/usetrove/trove/cmd/trove@latest');
  }
  return { goos, goarch };
}

function archiveName(version, t) {
  return `trove_${version}_${t.goos}_${t.goarch}.tar.gz`;
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
    mod.get(url, { headers: { 'user-agent': 'trove-cli' } }, (res) => {
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
  return process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache');
}

// Returns the path to a verified trove binary, downloading it if not cached.
async function ensureBinary({ version, baseUrl = DEFAULT_BASE, cacheDir = path.join(cacheRoot(), 'trove-cli'), platform, arch } = {}) {
  if (!version || version === '0.0.0') {
    throw new Error('This trove-cli package has no release version. Install a published version: npm install -g trove-cli');
  }
  const t = target(platform, arch);
  const bin = path.join(cacheDir, version, 'trove');
  if (fs.existsSync(bin)) return bin;

  const file = archiveName(version, t);
  const root = `${baseUrl}/v${version}`;
  const [archive, sums] = await Promise.all([get(`${root}/${file}`), get(`${root}/checksums.txt`)]);
  verify(archive, sums.toString('utf8'), file);

  const dir = path.join(cacheDir, version);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const tmp = fs.mkdtempSync(path.join(dir, 'x-'));
  try {
    const tgz = path.join(tmp, file);
    fs.writeFileSync(tgz, archive);
    execFileSync('tar', ['-xzf', tgz, '-C', tmp, 'trove'], { stdio: 'ignore' });
    fs.chmodSync(path.join(tmp, 'trove'), 0o755);
    fs.renameSync(path.join(tmp, 'trove'), bin);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  return bin;
}

module.exports = { target, archiveName, expectedChecksum, verify, ensureBinary, DEFAULT_BASE };
