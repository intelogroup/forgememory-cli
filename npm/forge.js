#!/usr/bin/env node
'use strict';

const { spawn, spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');

const isWindows = os.platform() === 'win32';
const ext = isWindows ? '.exe' : '';
const localBinary = path.join(__dirname, 'bin', `forgememo${ext}`);

const args = process.argv.slice(2);

if (args[0] === 'setup') {
  runSetup();
} else {
  proxy(args);
}

function proxy(args) {
  if (!fs.existsSync(localBinary)) {
    console.error('forgememo binary not found. Re-run: npm install -g forgememo-cli');
    process.exit(1);
  }
  if (shouldDetachCli(args)) {
    runDetached(localBinary, args);
    return;
  }
  const result = spawnSync(localBinary, args, { stdio: 'inherit' });
  process.exit(result.status ?? 1);
}

function shouldDetachCli(args) {
  return isWindows && args[0] === 'start';
}

function runDetached(binary, args) {
  // `spawnSync()` on Windows keeps the CLI under Node's process tree/job object.
  // That works for short-lived commands, but it can take down the daemon that
  // `forgememo start` launches after the CLI itself exits. Running the CLI in a
  // detached child keeps the daemon lifecycle tied to the Go binary instead.
  const child = spawn(binary, args, {
    detached: true,
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  });

  child.stdout.on('data', (chunk) => {
    process.stdout.write(chunk);
  });
  child.stderr.on('data', (chunk) => {
    process.stderr.write(chunk);
  });
  child.on('error', (err) => {
    console.error(err.message);
    process.exit(1);
  });
  child.on('close', (code, signal) => {
    if (signal) {
      console.error(`forgememo exited from signal: ${signal}`);
      process.exit(1);
    }
    process.exit(code ?? 1);
  });
}

function runSetup() {
  if (!fs.existsSync(localBinary)) {
    console.error('forgememo binary not found. The postinstall may have failed — check your network.');
    process.exit(1);
  }

  // Install binary to ~/.forgememo/bin/forgememo
  const destDir = path.join(os.homedir(), '.forgememo', 'bin');
  const destBinary = path.join(destDir, `forgememo${ext}`);

  fs.mkdirSync(destDir, { recursive: true });
  fs.copyFileSync(localBinary, destBinary);
  if (!isWindows) fs.chmodSync(destBinary, 0o755);

  console.log(`forgememo installed to: ${destBinary}`);

  // PATH instructions
  const inPath = (process.env.PATH || '').split(path.delimiter).includes(destDir);
  if (!inPath) {
    console.log('');
    console.log('Add forgememo to your PATH:');
    if (isWindows) {
      console.log(`  [System.Environment]::SetEnvironmentVariable("PATH", $env:PATH + ";${destDir}", "User")`);
    } else {
      console.log(`  echo 'export PATH="$HOME/.forgememo/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc`);
    }
    console.log('');
  }

  // Run forgememo init + forgememo start
  const init = spawnSync(destBinary, ['init'], { stdio: 'inherit' });
  if (init.status !== 0) process.exit(init.status ?? 1);

  if (shouldDetachCli(['start'])) {
    runDetached(destBinary, ['start']);
    return;
  }

  const start = spawnSync(destBinary, ['start'], { stdio: 'inherit' });
  process.exit(start.status ?? 1);
}
