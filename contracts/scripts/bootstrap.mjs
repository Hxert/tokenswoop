import { mkdirSync, readFileSync, writeFileSync, existsSync, chmodSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
const lock = JSON.parse(readFileSync('dependencies.lock.json', 'utf8'));
if (process.platform !== 'darwin' || process.arch !== 'arm64') {
  throw new Error(`This checksum-pinned bootstrap targets macOS arm64. Install Foundry ${lock.foundry.version} and solc ${lock.solidity.version} for your platform, place them in .toolchain/bin, then fetch the locked library commits.`);
}
mkdirSync('.toolchain/bin', {recursive: true});
mkdirSync('lib', {recursive: true});
const downloads = [
  {name: 'foundry', url: 'https://github.com/foundry-rs/foundry/releases/download/v1.8.0/foundry_v1.8.0_darwin_arm64.tar.gz', path: '.toolchain/foundry.tar.gz', sha: lock['darwin-arm64'].foundrySha256, target: '.toolchain/bin'},
  {name: 'openzeppelin', url: `https://api.github.com/repos/OpenZeppelin/openzeppelin-contracts/tarball/${lock.openzeppelin.commit}`, path: '.toolchain/openzeppelin.tar.gz', sha: lock.openzeppelin.archiveSha256, target: 'lib/openzeppelin-contracts', strip: true},
  {name: 'forge-std', url: `https://api.github.com/repos/foundry-rs/forge-std/tarball/${lock['forge-std'].commit}`, path: '.toolchain/forge-std.tar.gz', sha: lock['forge-std'].archiveSha256, target: 'lib/forge-std', strip: true},
  {name: 'solc', url: `https://github.com/argotorg/solidity/releases/download/v${lock.solidity.version}/solc-macos`, path: `.toolchain/bin/solc-${lock.solidity.version}`, sha: lock['darwin-arm64'].solcSha256}
];
for (const item of downloads) {
  let bytes;
  if (existsSync(item.path)) bytes = readFileSync(item.path);
  else {
    const response = await fetch(item.url, {signal: AbortSignal.timeout(120000)});
    if (!response.ok) throw new Error(`${item.name}: HTTP ${response.status}`);
    bytes = Buffer.from(await response.arrayBuffer());
  }
  if (createHash('sha256').update(bytes).digest('hex') !== item.sha) throw new Error(`${item.name}: checksum mismatch`);
  writeFileSync(item.path, bytes);
  if (item.target) {
    mkdirSync(item.target, {recursive: true});
    execFileSync('/usr/bin/tar', ['-xzf', item.path, '-C', item.target, ...(item.strip ? ['--strip-components=1'] : [])]);
  }
  console.log(`${item.name}: checksum verified`);
}
for (const name of ['forge', 'cast', 'anvil', `solc-${lock.solidity.version}`]) chmodSync(`.toolchain/bin/${name}`, 0o755);
console.log(execFileSync('.toolchain/bin/forge', ['--version'], {encoding: 'utf8'}).trim());
console.log(execFileSync(`.toolchain/bin/solc-${lock.solidity.version}`, ['--version'], {encoding: 'utf8'}).trim());
