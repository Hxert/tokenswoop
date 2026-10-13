import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Read-only verification against local artifacts. Never submits source or transactions.
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
const deployment = JSON.parse(readFileSync('deployments/monad-testnet.json', 'utf8'));
const rpcUrl = process.env.MONAD_TESTNET_RPC_URL ?? deployment.rpc;
const cast = (...args) => execFileSync('.toolchain/bin/cast', args, {encoding: 'utf8'}).trim();
let lastRequest = 0, rpcId = 0;
async function rpc(method, params) {
  for (let attempt = 0; attempt < 6; ++attempt) {
    await new Promise(resolve => setTimeout(resolve, Math.max(0, lastRequest + 200 - Date.now())));
    lastRequest = Date.now();
    try {
      const response = await fetch(rpcUrl, {method: 'POST', headers: {'content-type': 'application/json'},
        body: JSON.stringify({jsonrpc: '2.0', id: ++rpcId, method, params}), signal: AbortSignal.timeout(15000)});
      if ([429, 502, 503, 504].includes(response.status)) throw new Error(`Retryable HTTP ${response.status}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const body = await response.json();
      if (body.error) throw new Error(body.error.message);
      return body.result;
    } catch (error) {
      if (attempt === 5 || !/Retryable|fetch failed|timeout|rate.limit|too many requests/i.test(error.message)) throw error;
      await new Promise(resolve => setTimeout(resolve, Math.min(500 * 2 ** attempt, 8000)));
    }
  }
}
function assert(condition, message) { if (!condition) throw new Error(message); }
function normalized(code, references) {
  const bytes = Buffer.from(code.slice(2), 'hex');
  for (const slots of Object.values(references)) for (const {start, length} of slots) bytes.fill(0, start, start + length);
  return `0x${bytes.toString('hex')}`;
}
const call = (to, signature, block) => rpc('eth_call', [{to, data: cast('sig', signature)}, block]);
const decodedAddress = value => `0x${value.slice(-40)}`.toLowerCase();

async function main() {
  const chainId = Number(BigInt(await rpc('eth_chainId', [])));
  assert(chainId === 10143 && deployment.chainId === chainId, 'Expected Monad Testnet chain 10143');
  const block = await rpc('eth_getBlockByNumber', ['finalized', false]);
  assert(block?.number, 'Finalized block unavailable');
  const contracts = {};
  for (const [name, address] of [['KUSDC', deployment.kUSDC], ['TokenSwoopPayG', deployment.payg], ['TokenSwoopPayGBatch', deployment.paygBatch]]) {
    const artifact = JSON.parse(readFileSync(`out/${name}.sol/${name}.json`, 'utf8'));
    if (deployment.compiler) assert(artifact.metadata.compiler.version === `${deployment.compiler.version}+commit.${deployment.compiler.commit}`,
      `${name}: local compiler differs from the recorded deployment compiler`);
    const code = await rpc('eth_getCode', [address, block.number]);
    assert(code !== '0x', `${name}: no deployed code`);
    const references = artifact.deployedBytecode.immutableReferences;
    const actual = normalized(code, references), expected = normalized(artifact.deployedBytecode.object, references);
    assert(actual === expected, `${name}: runtime bytecode differs from local artifact`);
    const deploymentHash = deployment.creationTransactions[name];
    const transaction = await rpc('eth_getTransactionByHash', [deploymentHash]);
    const creationReceipt = await rpc('eth_getTransactionReceipt', [deploymentHash]);
    assert(creationReceipt && BigInt(creationReceipt.status) === 1n
      && creationReceipt.contractAddress?.toLowerCase() === address.toLowerCase()
      && BigInt(creationReceipt.blockNumber) <= BigInt(block.number),
      `${name}: creation receipt does not identify this finalized contract`);
    const addressWord = value => value.slice(2).toLowerCase().padStart(64, '0');
    const constructorArguments = (name === 'KUSDC' ? '' : addressWord(deployment.kUSDC)) + addressWord(deployment.accounts.deployer) + (name === 'KUSDC' ? '' : addressWord(deployment.provider));
    assert(transaction && transaction.to === null
      && transaction.input.toLowerCase() === artifact.bytecode.object.toLowerCase() + constructorArguments,
      `${name}: creation transaction differs from the local artifact or constructor arguments`);
    const owner = decodedAddress(await call(address, 'owner()', block.number));
    const paused = BigInt(await call(address, 'paused()', block.number)) !== 0n;
    assert(owner === deployment.accounts.deployer.toLowerCase(), `${name}: unexpected initial owner`);
    assert(!paused, `${name}: paused`);
    contracts[name] = {address, compiler: artifact.metadata.compiler.version, runtimeBytes: (code.length - 2) / 2, runtimeMatchesLocalArtifact: true,
      creationMatchesLocalArtifact: true, creationTransactionHash: deploymentHash,
      constructorConfigurationChecked: true, normalizedRuntimeHash: cast('keccak', actual), owner, paused};
  }
  const decimals = Number(BigInt(await call(deployment.kUSDC, 'decimals()', block.number)));
  assert(decimals === 6, 'Payment token decimals mismatch');
  for (const [name, address] of [['TokenSwoopPayG', deployment.payg], ['TokenSwoopPayGBatch', deployment.paygBatch]]) {
    const paymentToken = decodedAddress(await call(address, 'paymentToken()', block.number));
    assert(paymentToken === deployment.kUSDC.toLowerCase(), 'Payment token mismatch');
    contracts[name].paymentToken = paymentToken;
    const provider = decodedAddress(await call(address, 'provider()', block.number));
    assert(provider === deployment.provider.toLowerCase(), 'Immutable Provider mismatch');
    contracts[name].provider = provider;
  }
  assert(deployment.providerMode === 'fixed-single' && deployment.paymentMode === 'token-allowance' && deployment.abiVersions?.TokenSwoopPayG === 4 && deployment.abiVersions?.TokenSwoopPayGBatch === 5 && deployment.settlementMode === 'bill-total', 'Expected the bill-total deployment manifest');
  contracts.TokenSwoopPayGBatch.settlementMode = 'bill-total';
  for (const name of ['TokenSwoopPayG','TokenSwoopPayGBatch']) contracts[name].paymentMode = 'token-allowance';
  const expectedDomain = cast('keccak', cast('abi-encode', 'domain(bytes32,bytes32,bytes32,uint256,address)',
    cast('keccak', 'EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)'),
    cast('keccak', 'TokenSwoop Test USDC'), cast('keccak', '2'), String(chainId), deployment.kUSDC));
  const domain = await call(deployment.kUSDC, 'DOMAIN_SEPARATOR()', block.number);
  assert(domain.toLowerCase() === expectedDomain.toLowerCase(), 'EIP-712 domain mismatch');
  contracts.KUSDC.decimals = decimals;
  contracts.KUSDC.domainSeparator = domain;
  const report = {chainId, rpc: rpcUrl, finalizedBlock: BigInt(block.number).toString(), checkedAt: new Date().toISOString(),
    method: 'Read-only RPC; check recorded compiler, compare creation transaction input including constructor arguments, compare runtime bytecode with compiler immutable ranges masked, then check owner, pause, decimals, payment token and EIP-712 domain.',
    contracts, sourceSubmittedForVerification: false};
  mkdirSync('reports', {recursive: true});
  writeFileSync('reports/deployment-verification.json', JSON.stringify(report, null, 2) + '\n');
  console.log(`Deployment matches local artifacts at finalized block ${report.finalizedBlock}. Report: reports/deployment-verification.json`);
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
