'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { target, archiveName, binaryName, windowsTar, expectedChecksum, verify, ensureBinary } = require('../lib/install');

const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

test('target maps node platform/arch to release names', () => {
  assert.deepEqual(target('darwin', 'arm64'), { goos: 'darwin', goarch: 'arm64' });
  assert.deepEqual(target('linux', 'x64'), { goos: 'linux', goarch: 'amd64' });
  assert.deepEqual(target('win32', 'x64'), { goos: 'windows', goarch: 'amd64' });
  assert.deepEqual(target('win32', 'arm64'), { goos: 'windows', goarch: 'arm64' });
  assert.throws(() => target('freebsd', 'x64'), /no prebuilt binary for freebsd\/x64/);
  assert.throws(() => target('linux', 'ia32'), /go install/);
});

test('archive name matches goreleaser name_template', () => {
  assert.equal(archiveName('1.2.3', { goos: 'linux', goarch: 'amd64' }), 'trove_1.2.3_linux_amd64.tar.gz');
  assert.equal(archiveName('1.2.3', { goos: 'windows', goarch: 'amd64' }), 'trove_1.2.3_windows_amd64.zip');
  assert.equal(binaryName({ goos: 'windows' }), 'trove.exe');
  assert.equal(binaryName({ goos: 'linux' }), 'trove');
});

test('expectedChecksum finds the right line and rejects unknown files', () => {
  const a = 'a'.repeat(64);
  const b = 'b'.repeat(64);
  const text = `${a}  one.tar.gz\n${b}  two.tar.gz\n`;
  assert.equal(expectedChecksum(text, 'two.tar.gz'), b);
  assert.throws(() => expectedChecksum(text, 'three.tar.gz'), /no entry for three/);
});

test('verify rejects a tampered download', () => {
  const data = Buffer.from('hello');
  const sums = `${sha(data)}  f.tar.gz\n`;
  verify(data, sums, 'f.tar.gz');
  assert.throws(() => verify(Buffer.from('HELLO'), sums, 'f.tar.gz'), /Checksum mismatch/);
});

function makeArchive(dir, version) {
  const src = fs.mkdtempSync(path.join(dir, 'src-'));
  fs.writeFileSync(path.join(src, 'trove'), '#!/bin/sh\necho fake-trove\n');
  const name = `trove_${version}_linux_amd64.tar.gz`;
  const out = path.join(dir, name);
  execFileSync('tar', ['-czf', out, '-C', src, 'trove']);
  return { name, data: fs.readFileSync(out) };
}

function serve(files) {
  let hits = 0;
  const srv = http.createServer((req, res) => {
    hits++;
    const f = files[req.url];
    if (!f) { res.statusCode = 404; return res.end(); }
    res.end(f);
  });
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve({
    base: `http://127.0.0.1:${srv.address().port}`,
    hits: () => hits,
    close: () => srv.close(),
  })));
}

test('ensureBinary downloads, verifies, extracts and caches', { skip: process.platform === 'win32' }, async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'trove-npm-'));
  const { name, data } = makeArchive(tmp, '1.0.0');
  const srv = await serve({
    [`/v1.0.0/${name}`]: data,
    '/v1.0.0/checksums.txt': `${sha(data)}  ${name}\n`,
  });
  try {
    const opts = { version: '1.0.0', baseUrl: srv.base, cacheDir: path.join(tmp, 'cache'), platform: 'linux', arch: 'x64' };
    const bin = await ensureBinary(opts);
    assert.ok(fs.existsSync(bin));
    assert.match(fs.readFileSync(bin, 'utf8'), /fake-trove/);
    const before = srv.hits();
    assert.equal(await ensureBinary(opts), bin);
    assert.equal(srv.hits(), before, 'second call must use the cache');
  } finally { srv.close(); }
});

test('ensureBinary refuses a binary whose checksum does not match and caches nothing', { skip: process.platform === 'win32' }, async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'trove-npm-'));
  const { name, data } = makeArchive(tmp, '1.0.1');
  const srv = await serve({
    [`/v1.0.1/${name}`]: data,
    '/v1.0.1/checksums.txt': `${'0'.repeat(64)}  ${name}\n`,
  });
  try {
    const cacheDir = path.join(tmp, 'cache');
    await assert.rejects(
      ensureBinary({ version: '1.0.1', baseUrl: srv.base, cacheDir, platform: 'linux', arch: 'x64' }),
      /Checksum mismatch/);
    assert.ok(!fs.existsSync(path.join(cacheDir, '1.0.1', 'trove')));
  } finally { srv.close(); }
});

test('ensureBinary reports a missing release in plain English', async () => {
  const srv = await serve({});
  try {
    await assert.rejects(
      ensureBinary({ version: '9.9.9', baseUrl: srv.base, cacheDir: fs.mkdtempSync(path.join(os.tmpdir(), 'trove-npm-')), platform: 'linux', arch: 'x64' }),
      /HTTP 404/);
  } finally { srv.close(); }
});

test('unpublished package version is rejected', async () => {
  await assert.rejects(ensureBinary({ version: '0.0.0' }), /no release version/);
});

// Windows: the release is a .zip holding trove.exe. This builds a real zip with the tar that ships with Windows 10 and later
// (bsdtar, which is also what the package uses to unpack it), so it only runs on Windows.
test('ensureBinary installs a Windows zip release', { skip: process.platform !== 'win32' }, async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'trove-npm-'));
  const src = fs.mkdtempSync(path.join(tmp, 'src-'));
  fs.writeFileSync(path.join(src, 'trove.exe'), 'fake-trove-windows');
  const name = 'trove_1.0.2_windows_amd64.zip';
  execFileSync(windowsTar(), ['-a', '-cf', path.join(tmp, name), '-C', src, 'trove.exe']);
  const data = fs.readFileSync(path.join(tmp, name));
  const srv = await serve({
    [`/v1.0.2/${name}`]: data,
    '/v1.0.2/checksums.txt': `${sha(data)}  ${name}
`,
  });
  try {
    const bin = await ensureBinary({ version: '1.0.2', baseUrl: srv.base, cacheDir: path.join(tmp, 'cache'), platform: 'win32', arch: 'x64' });
    assert.equal(path.basename(bin), 'trove.exe');
    assert.equal(fs.readFileSync(bin, 'utf8'), 'fake-trove-windows');
  } finally { srv.close(); }
});
