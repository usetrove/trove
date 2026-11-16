#!/usr/bin/env node
'use strict';
const { spawn } = require('node:child_process');
const { ensureBinary } = require('../lib/install');
const { version } = require('../package.json');

ensureBinary({ version }).then((bin) => {
  const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });
  for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child.kill(sig));
  child.on('exit', (code, signal) => process.exit(signal ? 1 : code));
  child.on('error', (e) => { console.error(`Could not start trove: ${e.message}`); process.exit(1); });
}).catch((e) => {
  console.error(`trove: ${e.message}`);
  process.exit(1);
});
