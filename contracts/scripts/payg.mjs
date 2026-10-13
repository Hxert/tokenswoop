import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
const args = process.argv.slice(2);
const command = args[0] ?? 'help';
const option = (name, fallback) => {
  const index = args.indexOf(`--${name}`);
  if (index < 0) return fallback;
  if (!args[index + 1] || args[index + 1].startsWith('--')) throw new Error(`Missing --${name} value`);
  return args[index + 1];
};
const local = args.includes('--local');
const contractMode = option('contract', 'single');
if (!['single', 'batch'].includes(contractMode)) throw new Error('--contract must be single or batch');
if (args.includes('--max')) throw new Error('Per-charge limits were removed; approve the payment contract on the token');
if (command === 'authorize' || ['--limit', '--expires-in', '--authorization-id'].some(value => args.includes(value))) throw new Error('Secondary authorization was removed; use approve --amount and omit authorization/version/expiry options');
if (args.includes('--request-id')) throw new Error('Onchain payments use bill IDs; use --bill-id for both charge and charge-batch');
if (args.includes('--requests')) throw new Error('Batch now accepts a bill total; use --bill-id and --amount instead of a request array');
const rpcUrl = option('rpc', local ? 'http://127.0.0.1:8545' : process.env.MONAD_TESTNET_RPC_URL ?? 'https://testnet-rpc.monad.xyz');
const expectedChain = local ? 31337 : 10143;
const manifestPath = option('manifest', local ? 'deployments/local.json' : 'deployments/monad-testnet.json');
const walletFile = option('wallets', 'deployments/testnet-wallets.json');
const cast = (...input) => execFileSync('.toolchain/bin/cast', input, {encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']}).trim();
const address = value => {
  if (!/^0x[0-9a-fA-F]{40}$/.test(value ?? '')) throw new Error('Invalid address');
  return value;
};
const word = value => BigInt(value).toString(16).padStart(64, '0');
const addressWord = value => address(value).slice(2).toLowerCase().padStart(64, '0');
const bytes32Hex = (value, label = 'bill-id') => {
  if (!/^0x[0-9a-fA-F]{64}$/.test(value ?? '')) throw new Error(`${label} must be a bytes32 hex string; retries must reuse it`);
  return value;
};
const amount = value => {
  if (typeof value !== 'string' || !/^\d+(\.\d{1,6})?$/.test(value)) throw new Error('Amount must be a decimal string with at most 6 decimals; no floats or exponent notation');
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
};
const formatUnits = (value, decimals = 6) => {
  const text = BigInt(value).toString().padStart(decimals + 1, '0');
  return `${text.slice(0, -decimals)}.${text.slice(-decimals)}`;
};
const save = (path, value) => {
  mkdirSync(dirname(path), {recursive: true});
  writeFileSync(path, JSON.stringify(value, (_, entry) => typeof entry === 'bigint' ? entry.toString() : entry, 2) + '\n');
};
let rpcId = 0;
let rpcTail = Promise.resolve();
let lastRpcAt = 0;
async function requestWithRetry(method, params) {
  for (let attempt = 0; attempt < 6; ++attempt) {
    await new Promise(resolve => setTimeout(resolve, Math.max(0, lastRpcAt + 150 - Date.now())));
    lastRpcAt = Date.now();
    try {
      const response = await fetch(rpcUrl, {method: 'POST', headers: {'content-type': 'application/json'},
        body: JSON.stringify({jsonrpc: '2.0', id: ++rpcId, method, params}), signal: AbortSignal.timeout(15000)});
      if ([429, 502, 503, 504].includes(response.status)) throw new Error(`Retryable RPC HTTP ${response.status}`);
      if (!response.ok) throw new Error(`RPC HTTP ${response.status}`);
      const body = await response.json();
      if (body.error) { const error = new Error(body.error.message); error.rpcError = body.error; throw error; }
      return body.result;
    } catch (error) {
      const transient = /Retryable|fetch failed|timeout|rate.limit|too many requests/i.test(error.message);
      if (!transient || attempt === 5) throw error;
      await new Promise(resolve => setTimeout(resolve, Math.min(500 * 2 ** attempt, 8000)));
    }
  }
}
function rpc(method, params) {
  const result = rpcTail.then(() => requestWithRetry(method, params));
  rpcTail = result.catch(() => {});
  return result;
}
async function checkChain() {
  const actual = Number(BigInt(await rpc('eth_chainId', [])));
  if (actual !== expectedChain) throw new Error(`Refusing chain ${actual}; expected ${expectedChain}`);
}
function wallets() {
  if (!existsSync(walletFile)) throw new Error('Run node scripts/wallets.mjs first, or provide --wallets with an existing local keystore manifest');
  const loaded = JSON.parse(readFileSync(walletFile, 'utf8'));
  for (const role of ['deployer', 'user', 'provider']) address(loaded.accounts[role]?.address);
  return loaded;
}
function manifest() {
  if (!existsSync(manifestPath)) throw new Error(`Missing ${manifestPath}; deploy first`);
  const loaded = JSON.parse(readFileSync(manifestPath, 'utf8'));
  if (loaded.chainId !== expectedChain) throw new Error('Deployment chain mismatch');
  if (loaded.providerMode !== 'fixed-single' || loaded.paymentMode !== 'token-allowance' || loaded.abiVersions?.TokenSwoopPayG !== 4 || loaded.abiVersions?.TokenSwoopPayGBatch !== 5) throw new Error('Use the allowance-only deployment and current ABI');
  address(loaded.provider);
  address(loaded.kUSDC); address(loaded.payg);
  if (contractMode === 'batch') loaded.payg = address(loaded.paygBatch);
  return loaded;
}
const signature = value => cast('sig', value);
async function call(to, functionSignature, encodedArgs = '', from) {
  return rpc('eth_call', [{to: address(to), data: signature(functionSignature) + encodedArgs, ...(from ? {from: address(from)} : {})}, 'latest']);
}
async function rejectNonProvider(deployment, accounts, id, batch) {
  const caller = accounts.accounts.deployer.address;
  const before = await snapshot(deployment, accounts, id);
  const json = value => JSON.stringify(value, (_, v) => typeof v === 'bigint' ? v.toString() : v);
  let errorData;
  try {
    await call(deployment.payg, batch ? 'chargeBatch(address,bytes32,uint256)' : 'charge(address,bytes32,uint256)',
      addressWord(accounts.accounts.user.address) + id.slice(2) + word(batch ? 12600 : 4200), caller);
  } catch (error) {
    if (!error.rpcError || !JSON.stringify(error.rpcError).toLowerCase().includes(signature('NotProvider(address)').toLowerCase())) throw error;
    errorData = error.rpcError.data;
  }
  assert(errorData, 'An unrelated caller was allowed to charge');
  assert(json(before) === json(await snapshot(deployment, accounts, id)), 'Rejected caller changed payment state');
  return {fixedProvider:deployment.provider, rejectedCaller:caller, method:'eth_call', broadcast:false, errorData};
}
async function receiptConfirmed(receipt) {
  if (BigInt(receipt.status) !== 1n) throw new Error(`Transaction failed: ${receipt.transactionHash}`);
  const latest = await rpc('eth_getTransactionReceipt', [receipt.transactionHash]);
  if (!latest || latest.blockHash !== receipt.blockHash || BigInt(latest.status) !== 1n) throw new Error('Receipt is missing or changed');
  if (!local) {
    const deadline = Date.now() + 45000;
    while (Date.now() < deadline) {
      const finalized = await rpc('eth_getBlockByNumber', ['finalized', false]);
      if (finalized && BigInt(finalized.number) >= BigInt(latest.blockNumber)) return latest;
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
    throw new Error(`Receipt awaiting finality: ${receipt.transactionHash}. Query it; do not create a new request ID.`);
  }
  return latest;
}
async function send(role, to, functionSignature, values, accounts, onReceipt) {
  const wallet = accounts.accounts[role];
  const output = cast('send', address(to), functionSignature, ...values.map(String), '--rpc-url', rpcUrl,
    '--chain', String(expectedChain), '--keystore', wallet.keystore, '--password-file', accounts.passwordFile,
    '--from', wallet.address, '--gas-estimate-multiplier', '120', '--json');
  const receipt = JSON.parse(output);
  save(`reports/transactions/${receipt.transactionHash}.json`, receipt);
  onReceipt?.(receipt);
  return receiptConfirmed(receipt);
}
async function snapshot(deployment, accounts, id) {
  const user = accounts.accounts.user.address, provider = deployment.provider;
  const [userBalance, providerBalance, custodyBalance, allowance, paid] = await Promise.all([
    call(deployment.kUSDC, 'balanceOf(address)', addressWord(user)),
    call(deployment.kUSDC, 'balanceOf(address)', addressWord(provider)),
    call(deployment.kUSDC, 'balanceOf(address)', addressWord(deployment.payg)),
    call(deployment.kUSDC, 'allowance(address,address)', addressWord(user) + addressWord(deployment.payg)),
    id ? call(deployment.payg, 'isCharged(address,bytes32)', addressWord(user) + bytes32Hex(id).slice(2)) : Promise.resolve('0x0')
  ]);
  return {userBalance: BigInt(userBalance), providerBalance: BigInt(providerBalance), custodyBalance: BigInt(custodyBalance), allowance: BigInt(allowance), paid: BigInt(paid) !== 0n};
}
function assert(condition, message) { if (!condition) throw new Error(message); }

async function main() {
  if (command === 'help') {
    console.log('Commands: status, deploy [--token ADDRESS --token-transaction HASH] [--single ADDRESS --single-transaction HASH], demo, approve --amount 10, charge --bill-id 0x... --amount 0.0042, charge-batch --bill-id 0x... --amount 0.0126 --contract batch, revoke (token approve 0), export-abi. --contract single|batch selects the payment contract. --local requires localhost Anvil chain 31337.');
    return;
  }
  if (command === 'export-abi') {
    for (const name of ['KUSDC', 'TokenSwoopPayG', 'TokenSwoopPayGBatch']) {
      const artifact = JSON.parse(readFileSync(`out/${name}.sol/${name}.json`, 'utf8'));
      save(`abi/${name}.json`, artifact.abi);
    }
    console.log('Exported local ABI files'); return;
  }
  if (local && !['127.0.0.1', 'localhost', '[::1]'].includes(new URL(rpcUrl).hostname)) throw new Error('--local requires a loopback RPC');
  await checkChain();
  const accounts = wallets();
  if (command === 'status') {
    const balances = {};
    for (const [role, wallet] of Object.entries(accounts.accounts)) balances[role] = {address: wallet.address, mon: formatUnits(await rpc('eth_getBalance', [wallet.address, 'latest']), 18)};
    const state = existsSync(manifestPath) ? await snapshot(manifest(), accounts) : undefined;
    console.log(JSON.stringify({chainId: expectedChain, rpc: rpcUrl, balances, state}, (_, value) => typeof value === 'bigint' ? value.toString() : value, 2)); return;
  }
  if (command === 'deploy') {
    if (existsSync(manifestPath)) throw new Error(`Deployment already recorded in ${manifestPath}; refusing an accidental redeployment`);
    const lock = JSON.parse(readFileSync('dependencies.lock.json', 'utf8'));
    execFileSync('.toolchain/bin/forge', ['build', '--silent'], {stdio: ['ignore', 'pipe', 'pipe']});
    for (const name of ['KUSDC', 'TokenSwoopPayG', 'TokenSwoopPayGBatch']) {
      const artifact = JSON.parse(readFileSync(`out/${name}.sol/${name}.json`, 'utf8'));
      assert(artifact.metadata.compiler.version === `${lock.solidity.version}+commit.${lock.solidity.commit}`, `${name}: compiler differs from the dependency lock`);
    }
    const deployer = accounts.accounts.deployer;
    const existingToken = option('token');
    const tokenTransaction = option('token-transaction');
    const existingPayG = option('single');
    const paygTransaction = option('single-transaction');
    if (existingToken) {
      address(existingToken);
      assert(existingToken.toLowerCase() !== '0x0000000000000000000000000000000000000000', 'Use a nonzero existing token address');
      bytes32Hex(tokenTransaction, 'token-transaction'); // A transaction hash is also exactly 32 bytes.
      assert(BigInt(await call(existingToken, 'managementVersion()')) === 1n, 'Use management-enabled kUSDC');
    }
    if (existingPayG) {
      assert(existingToken, '--single requires --token to reuse the same fixed payment token');
      address(existingPayG);
      assert(existingPayG.toLowerCase() !== '0x0000000000000000000000000000000000000000', 'Use a nonzero existing PayG address');
      bytes32Hex(paygTransaction, 'single-transaction');
      const artifact = JSON.parse(readFileSync('out/TokenSwoopPayG.sol/TokenSwoopPayG.json', 'utf8'));
      const deployed = await rpc('eth_getCode', [existingPayG, 'latest']);
      const normalize = code => {
        let result = code.replace(/^0x/, '').toLowerCase();
        for (const references of Object.values(artifact.deployedBytecode.immutableReferences)) for (const {start, length} of references) {
          result = result.slice(0, start*2) + '0'.repeat(length*2) + result.slice((start+length)*2);
        }
        return result;
      };
      assert(normalize(deployed) === normalize(artifact.deployedBytecode.object), 'Existing PayG bytecode differs from the current allowance-only contract; deploy a new one');
      assert(('0x'+(await call(existingPayG, 'owner()')).slice(-40)).toLowerCase() === deployer.address.toLowerCase(), 'Existing PayG has a different owner');
    }
    const contract = local ? 'DeployLocal' : 'DeployTestnet';
    // Intentionally no --verify and no verifier API key.
    const journalPath = option('deployment-journal', `reports/${local ? 'local' : 'testnet'}-deployment-progress.json`);
    if (args.includes('--resume')) {
      const previous = JSON.parse(readFileSync(journalPath,'utf8'));
      assert(previous.chainId === expectedChain && previous.provider === accounts.accounts.provider.address && previous.token === (existingToken ?? null), 'Resume configuration differs from the deployment journal');
    } else assert(!existsSync(journalPath),'Deployment was already attempted; inspect broadcast receipts and resume the same operation');
    save(journalPath,{chainId:expectedChain,provider:accounts.accounts.provider.address,token:existingToken ?? null,stage:'broadcasting',sourceSubmitted:false});
    const output = execFileSync('.toolchain/bin/forge', ['script', `script/DeployTestnet.s.sol:${contract}`, '--rpc-url', rpcUrl,
      '--broadcast', '--slow', ...(args.includes('--resume') ? ['--resume'] : []), '--sender', deployer.address, '--keystore', deployer.keystore, '--password-file', accounts.passwordFile,
      '--gas-estimate-multiplier', '120'], {encoding: 'utf8', maxBuffer: 10_000_000, env: {...process.env,
        DEPLOYER_ADDRESS: deployer.address, USER_ADDRESS: accounts.accounts.user.address, PROVIDER_ADDRESS: accounts.accounts.provider.address,
        PAYMENT_TOKEN_ADDRESS: existingToken ?? '0x0000000000000000000000000000000000000000',
        EXISTING_PAYG_ADDRESS: existingPayG ?? '0x0000000000000000000000000000000000000000'},
      stdio: ['ignore', 'pipe', 'pipe']});
    const broadcast = JSON.parse(readFileSync(`broadcast/DeployTestnet.s.sol/${expectedChain}/run-latest.json`, 'utf8'));
    const token = broadcast.transactions.find(tx => tx.contractName === 'KUSDC' && tx.transactionType === 'CREATE');
    const payg = broadcast.transactions.find(tx => tx.contractName === 'TokenSwoopPayG' && tx.transactionType === 'CREATE');
    const paygBatch = broadcast.transactions.find(tx => tx.contractName === 'TokenSwoopPayGBatch' && tx.transactionType === 'CREATE');
    if ((!token && !existingToken) || (!payg && !existingPayG) || !paygBatch) throw new Error('Missing deployment transactions; inspect local broadcast receipts before retrying');
    const receipts = [];
    for (const receipt of broadcast.receipts) receipts.push(await receiptConfirmed(receipt));
    // Match creation receipts by their deployed address. Receipt order can differ
    // from transaction order in a Foundry broadcast containing CREATE and CALL.
    const creationHash = deployed => {
      const matches = receipts.filter(receipt => receipt.contractAddress?.toLowerCase() === deployed.toLowerCase());
      assert(matches.length === 1, `Missing or ambiguous creation receipt for ${deployed}; inspect broadcast receipts before retrying`);
      return matches[0].transactionHash;
    };
    const tokenAddress = existingToken ?? token.contractAddress;
    const paygAddress = existingPayG ?? payg.contractAddress;
    for (const deployed of [tokenAddress, paygAddress, paygBatch.contractAddress]) assert(await rpc('eth_getCode', [deployed, 'latest']) !== '0x', 'No deployed code');
    const result = {chainId: expectedChain, rpc: rpcUrl, kUSDC: tokenAddress, payg: paygAddress, paygBatch: paygBatch.contractAddress,
      compiler: lock.solidity, tokenManagementVersion: 1, abiVersion: 5, abiVersions: {KUSDC: 2, TokenSwoopPayG: 4, TokenSwoopPayGBatch: 5},
      provider: accounts.accounts.provider.address, providerMode: 'fixed-single',
      settlementMode: 'bill-total', settlementId: 'billId', paymentMode: 'token-allowance', abiRevision: 'bill-id-naming', tokenReused: Boolean(existingToken), paygReused: Boolean(existingPayG),
      creationTransactions: {KUSDC: existingToken ? tokenTransaction : creationHash(tokenAddress), TokenSwoopPayG: existingPayG ? paygTransaction : creationHash(paygAddress), TokenSwoopPayGBatch: creationHash(paygBatch.contractAddress)},
      sourceVerified: false, createdAt: new Date().toISOString(), accounts: Object.fromEntries(Object.entries(accounts.accounts).map(([key, value]) => [key, value.address])),
      deploymentTransactions: receipts.map(receipt => ({hash: receipt.transactionHash, block: receipt.blockNumber, gasUsed: receipt.gasUsed}))};
    for (const address of [paygAddress,paygBatch.contractAddress]) assert(('0x'+(await call(address,'provider()')).slice(-40)).toLowerCase() === result.provider.toLowerCase(), 'Deployment fixed a different provider');
    save(manifestPath, result);
    save(journalPath,{...result,stage:'complete'});
    save(`reports/${local ? 'local' : 'testnet'}-deployment-receipts.json`, receipts);
    console.log(output.trim()); console.log(JSON.stringify(result, null, 2)); return;
  }
  const deployment = manifest();
  assert(deployment.provider.toLowerCase() === accounts.accounts.provider.address.toLowerCase(), 'Signing wallet does not match the fixed Provider');
  assert(('0x'+(await call(deployment.payg,'provider()')).slice(-40)).toLowerCase() === deployment.provider.toLowerCase(), 'Onchain Provider differs from the manifest');
  const user = accounts.accounts.user.address, provider = accounts.accounts.provider.address;
  if (command === 'approve') {
    console.log(JSON.stringify(await send('user', deployment.kUSDC, 'approve(address,uint256)', [deployment.payg, amount(option('amount', '10'))], accounts))); return;
  }
  if (command === 'charge') {
    const id = bytes32Hex(option('bill-id'));
    const state = await snapshot(deployment, accounts, id);
    if (state.paid) { console.log('This bill is already paid; no transaction sent.'); return; }
    console.log(JSON.stringify(await send('provider', deployment.payg, 'charge(address,bytes32,uint256)', [user, id, amount(option('amount', '0.0042'))], accounts))); return;
  }
  if (command === 'revoke') {
    console.log(JSON.stringify(await send('user', deployment.kUSDC, 'approve(address,uint256)', [deployment.payg, 0], accounts))); return;
  }
  if (command === 'charge-batch') {
    assert(contractMode === 'batch', 'charge-batch requires --contract batch');
    assert(deployment.settlementMode === 'bill-total', 'Use the current bill-total Batch deployment and ABI');
    const id = bytes32Hex(option('bill-id'), 'bill-id');
    const total = amount(option('amount'));
    assert(total > 0n, 'Bill total must be positive');
    const state = await snapshot(deployment, accounts, id);
    if (state.paid) { console.log('This bill is already paid; no transaction sent.'); return; }
    console.log(JSON.stringify(await send('provider', deployment.payg, 'chargeBatch(address,bytes32,uint256)',
      [user, id, total], accounts))); return;
  }
  if (command === 'demo') { if (contractMode === 'batch') await demoBatch(deployment, accounts); else await demo(deployment, accounts); return; }
  throw new Error(`Unknown command: ${command}`);
}

const jsonState = value => JSON.stringify(value, (_, v) => typeof v === 'bigint' ? v.toString() : v);
const restoredState = value => ({...value, userBalance:BigInt(value.userBalance), providerBalance:BigInt(value.providerBalance),
  custodyBalance:BigInt(value.custodyBalance), allowance:BigInt(value.allowance)});

async function recoverPaymentReceipt(deployment, user, id, batch, receipts) {
  const topic = cast('keccak', batch ? 'BatchCharged(address,address,bytes32,uint256)' : 'Charged(address,address,bytes32,uint256)');
  const matches = log => log.address.toLowerCase() === deployment.payg.toLowerCase() && log.topics[0] === topic && log.topics[3] === id;
  const saved = receipts.find(receipt => receipt.logs.some(matches));
  if (saved) return receiptConfirmed(saved);
  const first = BigInt(deployment.deploymentTransactions[0].block);
  let end = BigInt(await rpc('eth_blockNumber', []));
  while (end >= first) {
    const start = end - first > 99n ? end - 99n : first;
    const logs = await rpc('eth_getLogs', [{address:deployment.payg, fromBlock:'0x'+start.toString(16), toBlock:'0x'+end.toString(16),
      topics:[topic,'0x'+addressWord(user),'0x'+addressWord(deployment.provider),id]}]);
    if (logs.length) return receiptConfirmed(await rpc('eth_getTransactionReceipt', [logs[0].transactionHash]));
    if (start === first) break;
    end = start - 1n;
  }
  throw new Error('Paid ID found without its receipt; no further debit sent');
}

function verifyPaymentReceipt(deployment, user, id, value, batch, receipt) {
  const transfers = receipt.logs.filter(log => log.address.toLowerCase() === deployment.kUSDC.toLowerCase()
    && log.topics[0] === cast('keccak', 'Transfer(address,address,uint256)'));
  const payments = receipt.logs.filter(log => log.address.toLowerCase() === deployment.payg.toLowerCase()
    && log.topics[0] === cast('keccak', batch ? 'BatchCharged(address,address,bytes32,uint256)' : 'Charged(address,address,bytes32,uint256)'));
  assert(transfers.length === 1 && payments.length === 1, 'Expected one token transfer and one payment event');
  assert(transfers[0].topics[1] === '0x'+addressWord(user) && transfers[0].topics[2] === '0x'+addressWord(deployment.provider)
    && BigInt(transfers[0].data) === value, 'Incorrect token transfer');
  assert(payments[0].topics[1] === '0x'+addressWord(user) && payments[0].topics[2] === '0x'+addressWord(deployment.provider)
    && payments[0].topics[3] === id && BigInt(payments[0].data) === value, 'Incorrect payment event');
  return {tokenTransfers:transfers.length, paymentReceipts:payments.length};
}

async function demo(deployment, accounts) {
  const user = accounts.accounts.user.address, provider = deployment.provider;
  const prefix = local ? 'local' : 'testnet';
  const progressPath = `reports/${prefix}-demo-progress.json`;
  let progress = existsSync(progressPath) ? JSON.parse(readFileSync(progressPath, 'utf8')) : null;
  if (progress?.status === 'complete') progress = null;
  if (!progress) {
    const runId = randomUUID();
    progress = {chainId:expectedChain, kUSDC:deployment.kUSDC, payg:deployment.payg, user, provider, paymentMode:'token-allowance',
      runId, billIds:[1,2,3,4].map(index=>cast('keccak',`${runId}:bill:${index}`)), stage:0, status:'running', receipts:[], steps:[]};
    save(progressPath, progress);
  }
  if (!progress.billIds && progress.ids) {
    progress.billIds = progress.ids; delete progress.ids;
  }
  for (const step of progress.steps) if (step.requestId) {step.billId = step.requestId; delete step.requestId;}
  if (!progress.offchainBills) progress.offchainBills = progress.billIds.slice(0,3).map((billId,index)=>({billId,
    totalAmountUnits:'4200',requests:[{requestId:cast('keccak',progress.runId+':request:'+(index+1)),amountUnits:'4200'}]}));
  save(progressPath, progress);
  assert(progress.paymentMode === 'token-allowance' && progress.chainId === expectedChain
    && progress.payg.toLowerCase() === deployment.payg.toLowerCase() && progress.kUSDC.toLowerCase() === deployment.kUSDC.toLowerCase()
    && progress.user.toLowerCase() === user.toLowerCase() && progress.provider.toLowerCase() === provider.toLowerCase(),
    'Demo progress belongs to another deployment, ABI or wallet');
  const persist = () => save(progressPath, progress);
  const record = (role, to, sig, values) => send(role, to, sig, values, accounts, receipt => {
    if (!progress.receipts.some(saved=>saved.transactionHash===receipt.transactionHash)) progress.receipts.push(receipt);
    persist();
  });
  const stage = async (index, action) => {
    if (progress.stage > index) return;
    assert(progress.stage === index, 'Unexpected demo stage');
    await action(); progress.stage = index+1; persist();
  };
  const approveOnce = async value => {
    const current = BigInt(await call(deployment.kUSDC, 'allowance(address,address)', addressWord(user)+addressWord(deployment.payg)));
    if (current !== BigInt(value)) await record('user', deployment.kUSDC, 'approve(address,uint256)', [deployment.payg,value]);
  };
  const failed = async (id, expectedError) => {
    const before = await snapshot(deployment, accounts, id);
    let rejected = false;
    try { await call(deployment.payg, 'charge(address,bytes32,uint256)', addressWord(user)+id.slice(2)+word(4200), provider); }
    catch (error) {
      if (!error.rpcError || !JSON.stringify(error.rpcError).toLowerCase().includes(signature(expectedError).toLowerCase())) throw error;
      rejected = true;
    }
    assert(rejected, 'Expected payment to revert');
    const after = await snapshot(deployment, accounts, id);
    assert(jsonState(before) === jsonState(after), 'Failed payment changed balances, allowance or paid ID');
    return after;
  };
  await stage(0, () => approveOnce(10_000_000));
  await stage(1, async () => {progress.initial = await snapshot(deployment, accounts);});
  if (!progress.fixedProviderCheck) {
    progress.fixedProviderCheck = await rejectNonProvider(deployment, accounts, progress.billIds[0], false); persist();
  }
  const initial = restoredState(progress.initial);
  async function debit(id, index) {
    const before = await snapshot(deployment, accounts, id);
    const receipt = before.paid ? await recoverPaymentReceipt(deployment, user, id, false, progress.receipts)
      : await record('provider', deployment.payg, 'charge(address,bytes32,uint256)', [user,id,4200]);
    const after = await snapshot(deployment, accounts, id);
    const cumulative = BigInt(index+1)*4200n;
    assert(after.userBalance === initial.userBalance-cumulative && after.providerBalance === initial.providerBalance+cumulative, 'Balance mismatch');
    assert(after.allowance === (index===2 ? 10_000_000n-4200n : initial.allowance-cumulative)
      && after.paid && after.custodyBalance === 0n, 'Payment accounting mismatch');
    const logCounts = verifyPaymentReceipt(deployment, user, id, 4200n, false, receipt);
    if (!progress.receipts.some(saved=>saved.transactionHash===receipt.transactionHash)) progress.receipts.push(receipt);
    progress.steps.push({step:'paid',billId:id,transactionHash:receipt.transactionHash,...logCounts,state:after});
  }
  await stage(2, () => debit(progress.billIds[0],0));
  await stage(3, () => debit(progress.billIds[1],1));
  await stage(4, async () => {
    await failed(progress.billIds[0], 'RequestAlreadyCharged(bytes32)');
    progress.steps.push({step:'duplicate-bill-rejected',billId:progress.billIds[0],mode:'eth_call'});
  });
  await stage(5, () => approveOnce(0));
  await stage(6, async () => {
    await failed(progress.billIds[2], 'ERC20InsufficientAllowance(address,uint256,uint256)');
    progress.steps.push({step:'insufficient-allowance-rejected',billId:progress.billIds[2],mode:'eth_call'});
  });
  await stage(7, () => approveOnce(10_000_000));
  await stage(8, () => debit(progress.billIds[2],2));
  await stage(9, () => approveOnce(0));
  await stage(10, async () => {
    progress.final = await failed(progress.billIds[3], 'ERC20InsufficientAllowance(address,uint256,uint256)');
    progress.steps.push({step:'revoked-token-allowance-rejected',billId:progress.billIds[3],mode:'eth_call'});
  });
  const final = restoredState(progress.final);
  assert(final.allowance === 0n && final.userBalance === initial.userBalance-12600n
    && final.providerBalance === initial.providerBalance+12600n && !final.paid, 'Final demo state mismatch');
  save(`reports/${prefix}-demo.json`, {chainId:expectedChain,rpc:rpcUrl,payg:deployment.payg,runId:progress.runId,
    paymentMode:'token-allowance',settlementId:'billId',offchainBills:progress.offchainBills,billsCharged:3,paymentTransactions:3,initial,final,steps:progress.steps,receipts:progress.receipts,totalPaidKUSDC:'0.012600',
    fixedProviderCheck:progress.fixedProviderCheck});
  progress.status = 'complete'; persist();
  console.log(`Demo passed: approval only, 3 bills, duplicate rejection, same-ID retry, token revocation. Report: reports/${prefix}-demo.json`);
}

async function demoBatch(deployment, accounts) {
  assert(deployment.settlementMode === 'bill-total', 'Demo requires a bill-total Batch deployment');
  const user = accounts.accounts.user.address, provider = deployment.provider;
  const prefix = local ? 'local-batch' : 'testnet-batch';
  const progressPath = `reports/${prefix}-demo-progress.json`;
  let progress = existsSync(progressPath) ? JSON.parse(readFileSync(progressPath, 'utf8')) : null;
  if (progress?.status === 'complete') progress = null;
  if (!progress) {
    const runId = randomUUID();
    progress = {chainId:expectedChain,kUSDC:deployment.kUSDC,payg:deployment.payg,user,provider,paymentMode:'token-allowance',
      settlementMode:'bill-total',runId,billIds:[1,2,3].map(index=>cast('keccak',`${runId}:bill:${index}`)),
      offchainRequests:[1,2,3].map(index=>({requestId:cast('keccak',`${runId}:request:${index}`),amountUnits:'4200'})),
      stage:0,status:'running',receipts:[],steps:[]};
    save(progressPath, progress);
  }
  assert(progress.paymentMode === 'token-allowance' && progress.settlementMode === 'bill-total' && progress.chainId === expectedChain
    && progress.payg.toLowerCase() === deployment.payg.toLowerCase() && progress.kUSDC.toLowerCase() === deployment.kUSDC.toLowerCase()
    && progress.user.toLowerCase() === user.toLowerCase() && progress.provider.toLowerCase() === provider.toLowerCase(),
    'Bill progress belongs to another deployment, ABI or wallet');
  const ids = progress.billIds, total = 12600n;
  const persist = () => save(progressPath, progress);
  const record = (role, to, sig, values) => send(role, to, sig, values, accounts, receipt => {
    if (!progress.receipts.some(saved=>saved.transactionHash===receipt.transactionHash)) progress.receipts.push(receipt);
    persist();
  });
  const stage = async (index, action) => {
    if (progress.stage > index) return;
    assert(progress.stage === index, 'Unexpected bill demo stage');
    await action(); progress.stage = index+1; persist();
  };
  const approveOnce = async value => {
    const current = BigInt(await call(deployment.kUSDC, 'allowance(address,address)', addressWord(user)+addressWord(deployment.payg)));
    if (current !== BigInt(value)) await record('user', deployment.kUSDC, 'approve(address,uint256)', [deployment.payg,value]);
  };
  const state = async () => {
    const snapshot_ = await snapshot(deployment, accounts);
    const bills = [];
    for (const id of ids) bills.push(BigInt(await call(deployment.payg, 'isCharged(address,bytes32)', addressWord(user)+id.slice(2))) !== 0n);
    return {...snapshot_,bills};
  };
  const failed = async (id, expectedError, value=total, sig='chargeBatch(address,bytes32,uint256)') => {
    const before = await state();
    let rejected = false;
    try { await call(deployment.payg, sig, addressWord(user)+id.slice(2)+word(value), provider); }
    catch (error) {
      if (!error.rpcError || !JSON.stringify(error.rpcError).toLowerCase().includes(signature(expectedError).toLowerCase())) throw error;
      rejected = true;
    }
    assert(rejected, 'Expected bill to revert');
    assert(jsonState(before) === jsonState(await state()), 'Failed bill changed balances, allowance or paid ID');
  };
  await stage(0, () => approveOnce(0));
  await stage(1, async () => {progress.initial = await state();});
  await stage(2, async () => {
    await failed(ids[0], 'ERC20InsufficientAllowance(address,uint256,uint256)');
    progress.steps.push({step:'insufficient-allowance-bill-rejected',billId:ids[0],mode:'eth_call'});
  });
  await stage(3, () => approveOnce(10_000_000));
  if (!progress.fixedProviderCheck) {
    progress.fixedProviderCheck = await rejectNonProvider(deployment, accounts, ids[0], true); persist();
  }
  await stage(4, async () => {
    const before = await state();
    const receipt = before.bills[0] ? await recoverPaymentReceipt(deployment, user, ids[0], true, progress.receipts)
      : await record('provider', deployment.payg, 'chargeBatch(address,bytes32,uint256)', [user,ids[0],total]);
    const after = await state();
    assert(after.bills[0] && after.allowance === 10_000_000n-total
      && after.userBalance === BigInt(progress.initial.userBalance)-total
      && after.providerBalance === BigInt(progress.initial.providerBalance)+total && after.custodyBalance === 0n, 'Bill accounting mismatch');
    const logCounts = verifyPaymentReceipt(deployment, user, ids[0], total, true, receipt);
    const regularChargeReceipts = receipt.logs.filter(log=>log.address.toLowerCase() === deployment.payg.toLowerCase()
      && log.topics[0] === cast('keccak', 'Charged(address,address,bytes32,uint256)'));
    assert(regularChargeReceipts.length === 0, 'Bill emitted an additional Charged receipt');
    for (const request of progress.offchainRequests) assert(BigInt(await call(deployment.payg, 'isCharged(address,bytes32)',
      addressWord(user)+request.requestId.slice(2))) === 0n, 'Request details unexpectedly recorded on chain');
    if (!progress.receipts.some(saved=>saved.transactionHash===receipt.transactionHash)) progress.receipts.push(receipt);
    progress.steps.push({step:'bill-paid',billId:ids[0],totalAmount:total.toString(),transactionHash:receipt.transactionHash,
      tokenTransfers:logCounts.tokenTransfers,billReceipts:logCounts.paymentReceipts,regularChargeReceipts:0,state:after});
  });
  await stage(5, async () => {
    await failed(ids[0], 'RequestAlreadyCharged(bytes32)', 4200n);
    progress.steps.push({step:'duplicate-bill-rejected',billId:ids[0],mode:'eth_call'});
  });
  await stage(6, async () => {
    await failed(ids[0], 'RequestAlreadyCharged(bytes32)', 4200n, 'charge(address,bytes32,uint256)');
    progress.steps.push({step:'single-entry-cannot-replay-bill-id',billId:ids[0],mode:'eth_call'});
  });
  await stage(7, async () => {
    await failed(ids[1], 'ERC20InsufficientAllowance(address,uint256,uint256)', 10_000_000n);
    progress.steps.push({step:'bill-exceeding-remaining-allowance-rejected',billId:ids[1],mode:'eth_call'});
  });
  await stage(8, () => approveOnce(0));
  await stage(9, async () => {
    await failed(ids[2], 'ERC20InsufficientAllowance(address,uint256,uint256)');
    progress.final = await state();
    progress.steps.push({step:'revoked-token-allowance-bill-rejected',billId:ids[2],mode:'eth_call'});
  });
  assert(BigInt(progress.final.allowance) === 0n && jsonState(progress.final.bills) === jsonState([true,false,false]), 'Bill final state mismatch');
  save(`reports/${prefix}-demo.json`, {chainId:expectedChain,rpc:rpcUrl,payg:deployment.payg,runId:progress.runId,
    paymentMode:'token-allowance',settlementId:'billId',settlementMode:'bill-total',fixedProviderCheck:progress.fixedProviderCheck,billId:ids[0],
    offchainRequests:progress.offchainRequests,requestDetailsOnChain:false,initial:progress.initial,final:progress.final,
    steps:progress.steps,receipts:progress.receipts,totalPaidKUSDC:'0.012600',offchainRequestCount:3,billsCharged:1,paymentTransactions:1});
  progress.status = 'complete'; persist();
  console.log(`Bill-total demo passed: approval only, 1 bill, 1 transfer, same-ID retry, deduplication, token revocation. Report: reports/${prefix}-demo.json`);
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
