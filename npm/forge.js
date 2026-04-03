#!/usr/bin/env node
'use strict';

const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');

const isWindows = os.platform() === 'win32';
const ext = isWindows ? '.exe' : '';
const localBinary = path.join(__dirname, 'bin', `forge${ext}`);

const args = process.argv.slice(2);

if (args[0] === 'setup') {
  runSetup();
} else {
  proxy(args);
}

function proxy(args) {
  if (!fs.existsSync(localBinary)) {
    console.error('forge binary not found. Re-run: npm install -g forgememory');
    process.exit(1);
  }
  const result = spawnSync(localBinary, args, { stdio: 'inherit' });
  process.exit(result.status ?? 1);
}

function runSetup() {
  if (!fs.existsSync(localBinary)) {
    console.error('forge binary not found. The postinstall may have failed — check your network.');
    process.exit(1);
  }

  // Install binary to ~/.forge/bin/forge
  const destDir = path.join(os.homedir(), '.forge', 'bin');
  const destBinary = path.join(destDir, `forge${ext}`);

  fs.mkdirSync(destDir, { recursive: true });
  fs.copyFileSync(localBinary, destBinary);
  if (!isWindows) fs.chmodSync(destBinary, 0o755);

  console.log(`forge installed to: ${destBinary}`);

  // PATH instructions
  const inPath = (process.env.PATH || '').split(path.delimiter).includes(destDir);
  if (!inPath) {
    console.log('');
    console.log('Add forge to your PATH:');
    if (isWindows) {
      console.log(`  [System.Environment]::SetEnvironmentVariable("PATH", $env:PATH + ";${destDir}", "User")`);
    } else {
      console.log(`  echo 'export PATH="$HOME/.forge/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc`);
    }
    console.log('');
  }

  // Run forge init + forge start
  const init = spawnSync(destBinary, ['init'], { stdio: 'inherit' });
  if (init.status !== 0) process.exit(init.status ?? 1);

  spawnSync(destBinary, ['start'], { stdio: 'inherit' });
}
