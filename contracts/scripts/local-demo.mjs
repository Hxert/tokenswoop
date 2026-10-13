import {spawn, execFileSync} from 'node:child_process';
import {existsSync, rmSync, readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {dirname, resolve} from 'node:path';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
if (!existsSync('deployments/testnet-wallets.json')) throw new Error('Generate local encrypted keystores with node scripts/wallets.mjs first');
const rpcUrl = 'http://127.0.0.1:18545';
const anvil = spawn('.toolchain/bin/anvil', ['--host', '127.0.0.1', '--port', '18545', '--network', 'monad', '--chain-id', '31337', '--silent'], {stdio: ['ignore', 'ignore', 'pipe']});
let startupError = '';
anvil.stderr.on('data', data => { startupError += data.toString(); });
async function rpc(method, params) {
  const response = await fetch(rpcUrl, {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify({jsonrpc:'2.0',id:1,method,params}), signal: AbortSignal.timeout(1000)});
  const body = await response.json();
  if (body.error) throw new Error(body.error.message);
  return body.result;
}
try {
  let started = false;
  for (let attempt = 0; attempt < 50; ++attempt) {
    if (anvil.exitCode !== null) throw new Error(`Anvil failed to start: ${startupError}`);
    try { if (BigInt(await rpc('eth_chainId', [])) === 31337n) { started = true; break; } } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!started) throw new Error('Local Anvil did not start');
  if (anvil.exitCode !== null) throw new Error('Refusing to use an RPC not owned by this local demo');
  const wallets = JSON.parse(readFileSync('deployments/testnet-wallets.json', 'utf8'));
  for (const wallet of Object.values(wallets.accounts)) await rpc('anvil_setBalance', [wallet.address, '0x4563918244f40000']);
  if (existsSync('deployments/local.json')) rmSync('deployments/local.json');
  if (existsSync('deployments/local-reserve.json')) rmSync('deployments/local-reserve.json');
  // A newly started Anvil has no prior transactions; old progress cannot be resumed here.
  for (const mode of ['local', 'local-batch']) {
    const progress = `reports/${mode}-demo-progress.json`;
    if (existsSync(progress)) rmSync(progress);
  }
  for (const path of ['reports/local-deployment-progress.json', 'reports/local-reserve-demo-progress.json', 'reports/local-reserve-deployment-progress.json']) {
    if (existsSync(path)) rmSync(path);
  }
  for (const command of [['deploy'], ['demo'], ['demo', '--contract', 'batch']]) {
    const output = execFileSync(process.execPath, ['scripts/payg.mjs', ...command, '--local', '--rpc', rpcUrl], {encoding: 'utf8', maxBuffer: 10_000_000});
    console.log(output.trim());
  }
  for (const command of ['deploy', 'demo', 'verify']) {
    const output = execFileSync(process.execPath, ['scripts/reserve.mjs', command, '--local', '--rpc', rpcUrl], {encoding: 'utf8', maxBuffer: 10_000_000});
    console.log(output.trim());
  }
} finally {
  anvil.kill('SIGTERM');
}
