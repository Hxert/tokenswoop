import { mkdirSync, existsSync, readFileSync, writeFileSync, chmodSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
mkdirSync('.secrets/keystores', {recursive: true, mode: 0o700});
chmodSync('.secrets', 0o700);
chmodSync('.secrets/keystores', 0o700);
const passwordFile = '.secrets/keystore-password';
if (!existsSync(passwordFile)) writeFileSync(passwordFile, randomBytes(32).toString('hex'), {flag: 'wx', mode: 0o600});
chmodSync(passwordFile, 0o600);
const accounts = {};
for (const role of ['deployer', 'user', 'provider']) {
  const keystore = `.secrets/keystores/tokenswoop-${role}`;
  if (!existsSync(keystore)) {
    // Password goes through the subprocess environment, never shell interpolation or output.
    execFileSync('.toolchain/bin/cast', ['wallet', 'new', '.secrets/keystores', `tokenswoop-${role}`, '--json'], {
      env: {...process.env, CAST_PASSWORD: readFileSync(passwordFile, 'utf8').trim()}, stdio: ['ignore', 'pipe', 'pipe']
    });
  }
  chmodSync(keystore, 0o600);
  const address = execFileSync('.toolchain/bin/cast', ['wallet', 'address', '--keystore', keystore, '--password-file', passwordFile], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']
  }).trim();
  accounts[role] = {address, keystore};
}
mkdirSync('deployments', {recursive: true});
writeFileSync('deployments/testnet-wallets.json', JSON.stringify({chainId: 10143, passwordFile, accounts}, null, 2) + '\n', {mode: 0o600});
console.log(JSON.stringify(Object.fromEntries(Object.entries(accounts).map(([role, value]) => [role, value.address])), null, 2));
