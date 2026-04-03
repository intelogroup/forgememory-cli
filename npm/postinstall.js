#!/usr/bin/env node
'use strict';

const https = require('https');
const fs = require('fs');
const path = require('path');
const os = require('os');
const { execSync } = require('child_process');

const VERSION = require('./package.json').version;
const REPO = 'intelogroup/forgememory-cli';

function getPlatformInfo() {
  const plat = os.platform();
  const arch = os.arch();
  const osMap = { darwin: 'darwin', linux: 'linux', win32: 'windows' };
  const archMap = { x64: 'amd64', arm64: 'arm64' };
  const osName = osMap[plat];
  const archName = archMap[arch];
  if (!osName) throw new Error(`Unsupported OS: ${plat}`);
  if (!archName) throw new Error(`Unsupported arch: ${arch}`);
  return { osName, archName, isWindows: plat === 'win32' };
}

function download(url, dest) {
  return new Promise((resolve, reject) => {
    const follow = (u) => {
      https.get(u, (res) => {
        if (res.statusCode === 301 || res.statusCode === 302) {
          return follow(res.headers.location);
        }
        if (res.statusCode !== 200) {
          return reject(new Error(`HTTP ${res.statusCode} downloading ${u}`));
        }
        const file = fs.createWriteStream(dest);
        res.pipe(file);
        file.on('finish', () => file.close(resolve));
        file.on('error', reject);
      }).on('error', reject);
    };
    follow(url);
  });
}

async function main() {
  const { osName, archName, isWindows } = getPlatformInfo();
  const ext = isWindows ? '.exe' : '';
  const archiveExt = isWindows ? '.zip' : '.tar.gz';
  const archiveName = `forge-${osName}-${archName}${archiveExt}`;
  const url = `https://github.com/${REPO}/releases/download/v${VERSION}/${archiveName}`;

  const binDir = path.join(__dirname, 'bin');
  if (!fs.existsSync(binDir)) fs.mkdirSync(binDir, { recursive: true });

  const binaryPath = path.join(binDir, `forge${ext}`);
  if (fs.existsSync(binaryPath)) return; // already downloaded

  const archivePath = path.join(binDir, archiveName);

  process.stdout.write(`  Downloading forge v${VERSION} (${osName}/${archName})... `);

  try {
    await download(url, archivePath);
  } catch (err) {
    console.error(`\n  Failed: ${err.message}`);
    console.error(`  Install manually: https://github.com/${REPO}/releases`);
    return; // non-fatal — user can re-run
  }

  if (isWindows) {
    execSync(
      `powershell -NoProfile -command "Expand-Archive -Path '${archivePath}' -DestinationPath '${binDir}' -Force"`,
      { stdio: 'pipe' }
    );
    const extracted = path.join(binDir, `forge-${osName}-${archName}.exe`);
    if (fs.existsSync(extracted)) fs.renameSync(extracted, binaryPath);
  } else {
    execSync(`tar xzf "${archivePath}" -C "${binDir}"`, { stdio: 'pipe' });
    const extracted = path.join(binDir, `forge-${osName}-${archName}`);
    if (fs.existsSync(extracted)) fs.renameSync(extracted, binaryPath);
    fs.chmodSync(binaryPath, 0o755);
  }

  if (fs.existsSync(archivePath)) fs.unlinkSync(archivePath);
  console.log('done.');
}

main().catch((err) => {
  console.warn(`  Warning: ${err.message}`);
});
