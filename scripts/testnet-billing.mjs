import {testnetEnvironment} from './lib/network-env.mjs'
// Opt-in test harness, not the production settlement worker. Never uses mainnet.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync, mkdirSync, renameSync, unlinkSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { billFromFixture, verifyPayment } from './lib/testnet-bill.mjs';
import { testnetRPC } from './lib/testnet-rpc.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const contracts = join(root, 'contracts');
const [command = 'help', ...args] = process.argv.slice(2);
assert(args.length === 0 || (args.length === 2 && args[0] === '--run' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(args[1])), 'Usage: prepare|settle|status [--run NAME]');
const run = args[1] ?? 'phase-one';
const directory = join(contracts, 'reports/gateway-billing', run);
const fixturePath = join(directory, 'fixture.json');
const journalPath = join(directory, 'journal.json');
const reportPath = join(directory, 'report.json');
const read = path => JSON.parse(readFileSync(path, 'utf8'));
const save = (path, value) => {
  writeFileSync(path + '.tmp', JSON.stringify(value, null, 2) + '\n', {mode:0o600});
  renameSync(path + '.tmp', path);
};
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const hex = value => '0x' + BigInt(value).toString(16);
const cast = (...args) => {
  try {
    return execFileSync(join(contracts, '.toolchain/bin/cast'), args.map(String), {cwd:contracts, encoding:'utf8', stdio:['ignore','pipe','pipe'], timeout:45000}).trim();
  } catch {
    // Do not print subprocess arguments/output: wallet commands can contain signatures.
    throw new Error(`Local cast ${args[0]} failed; no private key or password has been logged`);
  }
};
let rpcID = 0, lastRequest = 0;
const deployment = read(join(contracts, 'deployments/monad-testnet.json'));
const wallets = read(join(contracts, 'deployments/testnet-wallets.json'));
const rpcURL = testnetRPC(root, deployment.rpc);
assert(new URL(rpcURL).protocol === 'https:', 'Testnet RPC must use HTTPS');
assert(deployment.chainId === 10143 && wallets.chainId === 10143, 'Only chain 10143 is allowed');
assert(deployment.provider.toLowerCase() === wallets.accounts.provider.address.toLowerCase(), 'Provider signing wallet mismatch');

async function rpc(method, params) {
  // Retry only reads here. Broadcast uncertainty is resolved using the saved hash.
  const attempts = method === 'eth_sendRawTransaction' ? 1 : 5;
  for (let n = 0; n < attempts; n++) {
    await sleep(Math.max(0, lastRequest + 180 - Date.now()));
    lastRequest = Date.now();
    try {
      const response = await fetch(rpcURL, {method:'POST', headers:{'content-type':'application/json'}, body:JSON.stringify({jsonrpc:'2.0', id:++rpcID, method, params}), signal:AbortSignal.timeout(15000)});
      if (!response.ok) throw new Error(`RPC HTTP ${response.status}`);
      const value = await response.json();
      if (value.error) {
        const error = new Error(`RPC ${method} failed`);
        error.rpcError = value.error;
        throw error;
      }
      assert(value.result !== undefined, 'Missing RPC result');
      return value.result;
    } catch (error) {
      if (error.rpcError || n + 1 === attempts) throw error;
      await sleep(Math.min(500 * 2 ** n, 4000));
    }
  }
}
async function chain() {
  assert(BigInt(await rpc('eth_chainId', [])) === 10143n, 'Refusing a non-testnet RPC');
}
const data = (signature, ...values) => cast('calldata', signature, ...values);
const call = (to, signature, values = [], from, block = 'latest') => rpc('eth_call', [{to, data:data(signature, ...values), ...(from ? {from} : {})}, block]);

async function snapshot(bill, block = 'latest') {
  const state = {};
  // Serial reads avoid public RPC rate limits and use the same block for balances.
  if (block === 'latest') block = (await rpc('eth_getBlockByNumber', ['finalized', false])).number;
  state.block = block;
  state.userBalance = BigInt(await call(bill.token, 'balanceOf(address)', [bill.user], undefined, block)).toString();
  state.providerBalance = BigInt(await call(bill.token, 'balanceOf(address)', [bill.recipient || bill.provider], undefined, block)).toString();
  state.allowance = BigInt(await call(bill.token, 'allowance(address,address)', [bill.user, bill.contract], undefined, block)).toString();
  state.paid = BigInt(await call(bill.contract, 'isCharged(address,bytes32)', [bill.user, bill.billId], undefined, block)) === 1n;
  return state;
}

async function validateDeployment(bill) {
  await chain();
  for (const [name, address] of [['KUSDC', bill.token], ['TokenSwoopPayGBatch', bill.contract]]) {
    const artifact = read(join(contracts, `out/${name}.sol/${name}.json`));
    const normalize = code => {
      const bytes = Buffer.from(code.replace(/^0x/, ''), 'hex');
      for (const entries of Object.values(artifact.deployedBytecode.immutableReferences ?? {})) {
        for (const {start, length} of entries) bytes.fill(0, start, start + length);
      }
      return bytes.toString('hex');
    };
    assert(normalize(await rpc('eth_getCode', [address, 'latest'])) === normalize(artifact.deployedBytecode.object), `${name} bytecode mismatch`);
    assert(BigInt(await call(address, 'paused()')) === 0n, `${name} is paused`);
  }
  assert(('0x' + (await call(bill.contract, 'provider()')).slice(-40)).toLowerCase() === bill.provider, 'Onchain provider mismatch');
  assert(('0x' + (await call(bill.contract, 'paymentToken()')).slice(-40)).toLowerCase() === bill.token, 'Onchain payment token mismatch');
  assert(BigInt(await call(bill.token, 'decimals()')) === 6n, 'Token decimals mismatch');
}

async function confirmed(hash) {
  for (let n = 0; n < 45; n++) {
    const receipt = await rpc('eth_getTransactionReceipt', [hash]);
    if (receipt) {
      assert(BigInt(receipt.status) === 1n, `Transaction reverted: ${hash}`);
      const finalized = await rpc('eth_getBlockByNumber', ['finalized', false]);
      if (BigInt(finalized.number) >= BigInt(receipt.blockNumber)) {
        const block = await rpc('eth_getBlockByNumber', [receipt.blockNumber, false]);
        assert(block.hash === receipt.blockHash, 'Receipt block is no longer canonical');
        return receipt;
      }
    }
    await sleep(1000);
  }
  throw new Error(`Confirmation pending for ${hash}; rerun the SAME run name, never create a replacement bill`);
}

async function send(journal, step, role, to, signature, values) {
  await chain();
  const from = wallets.accounts[role].address.toLowerCase();
  const calldata = data(signature, ...values);
  let entry = journal.transactions[step];
  if (!entry) {
    const nonce = await rpc('eth_getTransactionCount', [from, 'pending']);
    assert(BigInt(nonce) === BigInt(await rpc('eth_getTransactionCount', [from, 'latest'])), 'Test wallet has a pending transaction; wait before continuing');
    const gas = BigInt(await rpc('eth_estimateGas', [{from, to, data:calldata}])) * 120n / 100n;
    const price = BigInt(await rpc('eth_gasPrice', []));
    assert(gas * price < 50_000_000_000_000_000n, 'Refusing a test transaction above 0.05 MON maximum gas cost');
    const raw = cast('mktx', to, signature, ...values, '--chain', '10143', '--legacy', '--nonce', BigInt(nonce), '--gas-limit', gas, '--gas-price', price,
      '--rpc-url', rpcURL, '--keystore', wallets.accounts[role].keystore, '--password-file', wallets.passwordFile, '--from', from);
    assert(/^0x[0-9a-f]+$/i.test(raw), 'Missing signed transaction');
    entry = {from, to, data:calldata, nonce, hash:cast('keccak', raw), raw};
    journal.transactions[step] = entry;
    // Persist signed bytes and hash BEFORE broadcasting. Retries reuse both.
    save(journalPath, journal);
  }
  assert(entry.from === from && entry.to === to && entry.data === calldata && cast('keccak', entry.raw) === entry.hash, 'Journal transaction differs from the intended operation');
  if (!await rpc('eth_getTransactionReceipt', [entry.hash]) && !await rpc('eth_getTransactionByHash', [entry.hash])) {
    try {
      const hash = await rpc('eth_sendRawTransaction', [entry.raw]);
      assert(hash.toLowerCase() === entry.hash.toLowerCase(), 'Broadcast hash mismatch');
    } catch {
      // Unknown network outcomes never cause a fresh nonce or a different bill ID.
      console.log(`${step}: broadcast outcome pending; checking saved hash ${entry.hash}`);
    }
  }
  entry.receipt = await confirmed(entry.hash);
  save(journalPath, journal);
  console.log(`${step}: finalized ${entry.hash}`);
  return entry.receipt;
}

async function expectRevert(bill, from, signature, values, errorSignature) {
  try {
    await call(bill.contract, signature, values, from);
  } catch (error) {
    assert(error.rpcError && JSON.stringify(error.rpcError).toLowerCase().includes(cast('sig', errorSignature).toLowerCase()), `Expected ${errorSignature}`);
    return {expectedError:errorSignature, method:'eth_call', broadcast:false};
  }
  throw new Error(`Expected ${errorSignature}, but simulation succeeded`);
}

async function main() {
  if (command === 'help') {
    console.log('prepare|settle|status [--run NAME]. prepare signs a local login and exports metered fixtures; settle broadcasts exactly 0.000120 test kUSDC on chain 10143. Existing runs are always reused.');
    return;
  }
  assert(['prepare', 'settle', 'status'].includes(command), 'Unknown command');
  mkdirSync(directory, {recursive:true, mode:0o700});
  // One settlement wallet is shared by all named runs; serialize across runs too.
  const lock = join(dirname(directory), '.lock');
  writeFileSync(lock, String(process.pid), {flag:'wx', mode:0o600});
  try {
    if (command === 'prepare' && !existsSync(fixturePath)) {
      Object.assign(process.env, testnetEnvironment(root));
      const database = process.env.TEST_DATABASE_URL ?? process.env.DATABASE_URL;
      const redis = process.env.TEST_REDIS_URL ?? process.env.REDIS_URL;
      for (const url of [database, redis]) assert(url && ['127.0.0.1', 'localhost', '[::1]'].includes(new URL(url).hostname), 'Fixture export requires local PostgreSQL and Redis');
      const backend = join(root, 'backend');
      execFileSync('go', ['test', '-race', '-count=1', '-run', '^TestExportTestnetBillingFixture$', '-v', './internal/httpapi'], {cwd:backend, stdio:'inherit', env:{...process.env,
        TEST_DATABASE_URL:database, TEST_REDIS_URL:redis, TESTNET_BILLING_FIXTURE:fixturePath, TESTNET_CONTRACTS_DIR:contracts,
        GOCACHE:join(backend,'.cache/go-build'), GOMODCACHE:join(backend,'.cache/gomod'), GOPATH:join(backend,'.cache/gopath')}});
      assert(existsSync(fixturePath), 'No fixture was generated; integration tests may have been skipped');
    }
    assert(existsSync(fixturePath), 'Run prepare first');
    const fixture = read(fixturePath);
    const bill = billFromFixture(fixture, deployment, wallets.accounts.user.address);
    if (command === 'prepare') {
      console.log(JSON.stringify({run, bill, fixture:fixturePath, broadcast:false}, null, 2));
      return;
    }
    await validateDeployment(bill);
    if (command === 'status') {
      console.log(JSON.stringify({run, bill, state:await snapshot(bill), report:existsSync(reportPath) ? reportPath : null}, null, 2));
      return;
    }
    let journal;
    if (existsSync(journalPath)) {
      journal = read(journalPath);
      assert(JSON.stringify(journal.bill) === JSON.stringify(bill), 'Bill changed after preparation; refusing to pay');
    } else {
      const before = await snapshot(bill);
      assert(!before.paid && before.allowance === '0', 'Start only with an unpaid bill and zero Batch allowance');
      assert(BigInt(before.userBalance) >= BigInt(bill.amountUnits), 'Insufficient test tokens');
      journal = {bill, before, transactions:{}, checks:{}, startedAt:new Date().toISOString()};
      save(journalPath, journal);
    }
    if (journal.status === 'complete') {
      const beforeReplay = await snapshot(bill);
      const providerNonce = await rpc('eth_getTransactionCount', [bill.provider, 'latest']);
      const userNonce = await rpc('eth_getTransactionCount', [bill.user, 'latest']);
      const receipt = await confirmed(journal.transactions.charge.hash);
      verifyPayment(receipt, bill, {payment:cast('keccak','BatchCharged(address,address,bytes32,uint256)'), transfer:cast('keccak','Transfer(address,address,uint256)')});
      const afterReplay = await snapshot(bill);
      assert(afterReplay.paid, 'Completed bill is no longer paid');
      for (const field of ['userBalance','providerBalance','allowance']) assert(beforeReplay[field] === afterReplay[field], 'Wallet changed during replay verification');
      assert(providerNonce === await rpc('eth_getTransactionCount', [bill.provider, 'latest']) && userNonce === await rpc('eth_getTransactionCount', [bill.user, 'latest']), 'A wallet nonce changed during replay verification');
      const report = read(reportPath);
      report.rerun = {checkedAt:new Date().toISOString(), noBroadcast:true, balancesAndNoncesUnchanged:true, providerNonce:BigInt(providerNonce).toString(), userNonce:BigInt(userNonce).toString()};
      save(reportPath, report);
      console.log(`Already finalized; no transaction sent. Report: ${reportPath}`);
      return;
    }
    // If a payment was previously signed, do not replenish allowance on restart.
    await send(journal, 'approve', 'user', bill.token, 'approve(address,uint256)', [bill.contract, bill.amountUnits]);
    if (!journal.transactions.charge) {
      journal.checks.nonProviderRejected = await expectRevert(bill, wallets.accounts.deployer.address, 'chargeBatch(address,bytes32,uint256)', [bill.user,bill.billId,bill.amountUnits], 'NotProvider(address)');
      const tooLargeID = cast('keccak', `tokenswoop:insufficient-allowance:${bill.billId}`);
      journal.checks.insufficientAllowanceRejected = await expectRevert(bill, bill.provider, 'chargeBatch(address,bytes32,uint256)', [bill.user,tooLargeID,(BigInt(bill.amountUnits)+1n).toString()], 'ERC20InsufficientAllowance(address,uint256,uint256)');
      save(journalPath, journal);
    }
    const receipt = await send(journal, 'charge', 'provider', bill.contract, 'chargeBatch(address,bytes32,uint256)', [bill.user,bill.billId,bill.amountUnits]);
    verifyPayment(receipt, bill, {payment:cast('keccak','BatchCharged(address,address,bytes32,uint256)'), transfer:cast('keccak','Transfer(address,address,uint256)')});
    journal.checks.sameBillRejected = await expectRevert(bill, bill.provider, 'chargeBatch(address,bytes32,uint256)', [bill.user,bill.billId,bill.amountUnits], 'RequestAlreadyCharged(bytes32)');
    journal.checks.crossEntryReplayRejected = await expectRevert(bill, bill.provider, 'charge(address,bytes32,uint256)', [bill.user,bill.billId,bill.amountUnits], 'RequestAlreadyCharged(bytes32)');
    journal.checks.noAllowanceRejected = await expectRevert(bill, bill.provider, 'chargeBatch(address,bytes32,uint256)', [bill.user,cast('keccak', `tokenswoop:zero-allowance:${bill.billId}`),'1'], 'ERC20InsufficientAllowance(address,uint256,uint256)');
    const after = await snapshot(bill);
    assert(after.paid && after.allowance === '0', 'Paid flag or remaining allowance mismatch');
    assert(BigInt(journal.before.userBalance)-BigInt(after.userBalance) === BigInt(bill.amountUnits), 'User debit mismatch');
    assert(BigInt(after.providerBalance)-BigInt(journal.before.providerBalance) === BigInt(bill.amountUnits), 'Revenue recipient receipt mismatch');
    journal.status = 'complete';
    journal.after = after;
    journal.completedAt = new Date().toISOString();
    const transactions = Object.fromEntries(Object.entries(journal.transactions).map(([step, tx]) => {
      const cost = BigInt(tx.receipt.gasUsed) * BigInt(tx.receipt.effectiveGasPrice);
      return [step, {hash:tx.hash,block:BigInt(tx.receipt.blockNumber).toString(),gasUsed:BigInt(tx.receipt.gasUsed).toString(),gasCostMON:`${cost / 10n**18n}.${(cost % (10n**18n)).toString().padStart(18,'0')}`}];
    }));
    save(reportPath, {run, status:'complete', bill, before:journal.before, after, transactions, checks:journal.checks, gatewayChecks:fixture.checks, completedAt:journal.completedAt,
      scope:'Real finalized testnet transfers using deterministic local upstream usage and simulated confirmed wallet snapshots. Not production wallet funding, commercial supplier usage, or an automated settlement worker.'});
    save(journalPath, journal);
    console.log(`Passed: 4 requests, ${bill.amount} kUSDC, zero remaining allowance. Report: ${reportPath}`);
  } finally {
    unlinkSync(lock);
  }
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
