#!/usr/bin/env node
// Launcher: runs the native binary from the platform package npm installed
// through optionalDependencies (the esbuild pattern). Nothing is downloaded.
'use strict';
const { spawnSync } = require('node:child_process');
const path = require('node:path');

const pkg = `@meetstream/cli-${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'meetstream.exe' : 'meetstream';
let bin = process.env.MEETSTREAM_BINARY_PATH;
if (!bin) {
  try {
    bin = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), 'bin', exe);
  } catch {
    console.error(`meetstream: no binary for ${process.platform}-${process.arch} (${pkg} is not installed).`);
    console.error('Reinstall without --no-optional, or get a binary from https://github.com/meetstream-ai/cli/releases');
    process.exit(1);
  }
}
const r = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' });
if (r.error) { console.error(`meetstream: ${r.error.message}`); process.exit(1); }
process.exit(r.status === null ? 1 : r.status);
