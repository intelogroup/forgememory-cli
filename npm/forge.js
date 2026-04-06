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

// --- Version helpers ---

// Parse "forge 0.4.20" or "0.4.20" → [0,4,20]. Returns null on failure.
function parseVersion(str) {
  const m = (str || '').trim().match(/(\d+)\.(\d+)\.(\d+)/);
  return m ? [parseInt(m[1], 10), parseInt(m[2], 10), parseInt(m[3], 10)] : null;
}

// Returns -1 (a<b), 0 (equal), or 1 (a>b).
function compareVersions(a, b) {
  for (let i = 0; i < 3; i++) {
    if (a[i] < b[i]) return -1;
    if (a[i] > b[i]) return  1;
  }
  return 0;
}

// Run `binaryPath --version`, return raw stdout string or null on failure.
function getBinaryVersion(binaryPath) {
  try {
    const result = spawnSync(binaryPath, ['--version'], { encoding: 'utf8', timeout: 3000 });
    return result.stdout ? result.stdout.trim() : null;
  } catch (_) { return null; }
}

// Return all executable paths named `name` found in PATH dirs, deduplicated by realpath.
function findAllInPath(name) {
  const dirs = (process.env.PATH || '').split(path.delimiter);
  const found = [];
  const seen = new Set();
  const suffix = isWindows ? '.exe' : '';
  for (const dir of dirs) {
    const candidate = path.join(dir, name + suffix);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      let real;
      try { real = fs.realpathSync(candidate); } catch (_) { real = candidate; }
      if (!seen.has(real)) { seen.add(real); found.push(candidate); }
    } catch (_) {}
  }
  return found;
}

// Run forgememo init + forgememo start from a given binary path.
function runInitAndStart(binary) {
  const init = spawnSync(binary, ['init'], { stdio: 'inherit' });
  if (init.status !== 0) process.exit(init.status ?? 1);
  if (shouldDetachCli(['start'])) { runDetached(binary, ['start']); return; }
  const start = spawnSync(binary, ['start'], { stdio: 'inherit' });
  process.exit(start.status ?? 1);
}

function runSetup() {
  if (!fs.existsSync(localBinary)) {
    console.error('forgememo binary not found. The postinstall may have failed — check your network.');
    process.exit(1);
  }

  const destDir    = path.join(os.homedir(), '.forgememo', 'bin');
  const destBinary = path.join(destDir, `forgememo${ext}`);
  const pkgVersion = require('./package.json').version;
  const newVer     = parseVersion(pkgVersion);

  // --- Version check against the already-installed destination binary ---
  if (fs.existsSync(destBinary)) {
    const existingRaw = getBinaryVersion(destBinary);
    const existingVer = parseVersion(existingRaw);
    if (existingVer) {
      const cmp = compareVersions(existingVer, newVer);
      if (cmp === 0) {
        console.log(`forgememo v${pkgVersion} already installed at ${destBinary}. Skipping copy.`);
        runInitAndStart(destBinary);
        return;
      }
      if (cmp > 0) {
        console.error(
          `Error: installed forgememo (${existingRaw}) is newer than this package ` +
          `(v${pkgVersion}). Aborting to prevent downgrade.\n` +
          `Run: npm install -g forgememo-cli@latest  to get the newest version.`
        );
        process.exit(1);
      }
      // cmp < 0 → upgrading
      console.log(`Upgrading forgememo: ${existingRaw} → v${pkgVersion}`);
    }
  }

  // --- Warn about OTHER forge / forgememo binaries already in PATH ---
  let destReal;
  try { destReal = fs.existsSync(destBinary) ? fs.realpathSync(destBinary) : null; } catch (_) { destReal = null; }
  const others = [...findAllInPath('forge'), ...findAllInPath('forgememo')].filter(p => {
    let r;
    try { r = fs.realpathSync(p); } catch (_) { r = p; }
    return r !== destReal;
  });
  if (others.length > 0) {
    console.log('\nNote: other forge installations found in PATH:');
    for (const p of others) {
      const v = getBinaryVersion(p);
      console.log(`  ${p}  (${v || 'unknown version'})`);
    }
    console.log('These may conflict. Remove them or ensure ~/.forgememo/bin appears first in PATH.\n');
  }

  // --- Copy binary ---
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

  runInitAndStart(destBinary);
}
