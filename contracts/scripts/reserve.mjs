import {existsSync, readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
process.chdir(root);
const args = process.argv.slice(2), command = args[0] ?? 'help';
const option = (name, fallback) => {
  const i = args.indexOf(`--${name}`);
  if (i < 0) return fallback;
  if (!args[i + 1] || args[i + 1].startsWith('--')) throw new Error(`Missing --${name}`);
  return args[i + 1];
};
const local = args.includes('--local'), expectedChain = local ? 31337 : 10143;
const rpcUrl = option('rpc', local ? 'http://127.0.0.1:8545' : process.env.MONAD_TESTNET_RPC_URL ?? 'https://testnet-rpc.monad.xyz');
const prefix = local ? 'local' : 'monad-testnet';
const manifestPath = option('manifest', `deployments/${prefix}-reserve.json`);
const paymentPath = option('payment-manifest', `deployments/${prefix === 'local' ? 'local' : 'monad-testnet'}.json`);
const progressPath = `reports/${prefix}-reserve-demo-progress.json`;
const json = path => JSON.parse(readFileSync(path, 'utf8'));
const save = (path, value) => {
  mkdirSync(dirname(path), {recursive:true});
  writeFileSync(path, JSON.stringify(value, (_, v) => typeof v === 'bigint' ? v.toString() : v, 2) + '\n');
};
const cast = (...input) => execFileSync('.toolchain/bin/cast', input.map(String), {encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim();
const assert = (condition, message) => { if (!condition) throw new Error(message); };
const address = value => { assert(/^0x[0-9a-fA-F]{40}$/.test(value ?? ''), 'Invalid address'); return value; };
const amount = value => {
  assert(typeof value === 'string' && /^\d+(\.\d{1,6})?$/.test(value), 'Use decimal strings with at most 6 decimals');
  const [whole, fraction = ''] = value.split('.');
  const units = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
  assert(units < 2n ** 256n, 'Amount exceeds uint256');
  return units;
};
const positive = value => { const units = amount(value); assert(units > 0n, 'Amount must be positive'); return units; };
const integer = (value, label) => { assert(/^\d+$/.test(value ?? ''), `${label} must be an integer`); return BigInt(value); };
const words = value => { const result = value.slice(2).match(/.{64}/g); assert(result?.length, 'Empty ABI response'); return result.map(v => BigInt(`0x${v}`)); };
const decodedAddress = value => `0x${value.slice(-40)}`.toLowerCase();
let rpcTail = Promise.resolve(), rpcId = 0, lastRpcAt = 0;
function rpc(method, params) {
  const result = rpcTail.then(async () => {
    for (let attempt = 0; attempt < 6; ++attempt) {
      await new Promise(r => setTimeout(r, Math.max(0, lastRpcAt + 150 - Date.now())));
      lastRpcAt = Date.now();
      try {
        const response = await fetch(rpcUrl, {method:'POST',headers:{'content-type':'application/json'},
          body:JSON.stringify({jsonrpc:'2.0',id:++rpcId,method,params}),signal:AbortSignal.timeout(15000)});
        if ([429,502,503,504].includes(response.status)) throw new Error(`Retryable HTTP ${response.status}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const body = await response.json();
        if (body.error) { const error = new Error(body.error.message); error.rpcError = body.error; throw error; }
        return body.result;
      } catch (error) {
        if (attempt === 5 || !/Retryable|fetch failed|timeout|rate.limit|too many requests/i.test(error.message)) throw error;
        await new Promise(r => setTimeout(r, Math.min(500 * 2 ** attempt, 8000)));
      }
    }
  });
  rpcTail = result.catch(() => {});
  return result;
}
async function checkChain() {
  if (local) assert(['127.0.0.1','localhost','[::1]'].includes(new URL(rpcUrl).hostname), '--local requires loopback RPC');
  assert(Number(BigInt(await rpc('eth_chainId', []))) === expectedChain, `Refusing a chain other than ${expectedChain}`);
}
const call = (to, signature, values = [], from, block = 'latest') => rpc('eth_call', [{to:address(to),
  data:cast('calldata', signature, ...values.map(String)), ...(from ? {from:address(from)} : {})}, block]);
const uint = async (to, signature, values = []) => BigInt(await call(to, signature, values));
async function confirmed(receipt) {
  assert(receipt && BigInt(receipt.status) === 1n, 'Transaction did not succeed');
  const current = await rpc('eth_getTransactionReceipt', [receipt.transactionHash]);
  assert(current && current.blockHash === receipt.blockHash && BigInt(current.status) === 1n, 'Receipt changed or disappeared');
  if (!local) {
    const deadline = Date.now() + 45000;
    while (Date.now() < deadline) {
      const block = await rpc('eth_getBlockByNumber', ['finalized',false]);
      if (block && BigInt(block.number) >= BigInt(current.blockNumber)) return current;
      await new Promise(r => setTimeout(r, 1000));
    }
    throw new Error(`Awaiting finality: ${current.transactionHash}. Resume this same operation.`);
  }
  return current;
}
function wallets() {
  const result = json(option('wallets','deployments/testnet-wallets.json'));
  for (const role of ['deployer','user','provider']) address(result.accounts[role]?.address);
  return result;
}
function deployment() {
  const d = json(manifestPath);
  assert(d.chainId === expectedChain && d.testnetMock === true, 'Expected the reserve TESTNET MOCK manifest');
  assert(d.abiVersion === 2 && d.tierManagement === 'provider', 'Use the principal-only Reserve v2 deployment; v1 has a different ABI');
  return d;
}
async function send(accounts, role, to, signature, values, onReceipt) {
  const wallet = accounts.accounts[role];
  const receipt = JSON.parse(cast('send',address(to),signature,...values.map(String),'--rpc-url',rpcUrl,'--chain',expectedChain,
    '--keystore',wallet.keystore,'--password-file',accounts.passwordFile,'--from',wallet.address,'--gas-estimate-multiplier','120','--json'));
  save(`reports/transactions/${receipt.transactionHash}.json`, receipt);
  onReceipt?.(receipt); // Persist before waiting for finality so a timeout cannot duplicate a deposit.
  return confirmed(receipt);
}
async function withdrawal(d, user) {
  const values = words(await call(d.reserve,'getWithdrawal(address)',[user]));
  assert(values.length === 6, 'Unexpected withdrawal ABI');
  const [requestId,amount,operatorVersion,requestedAt,approvedUntil,active] = values;
  return {requestId,amount,operatorVersion,requestedAt,approvedUntil,active:active !== 0n};
}
async function snapshot(d, accounts) {
  const user = accounts.accounts.user.address;
  const [principal,totalPrincipal,assets,yieldAvailable,userBalance,treasuryBalance,tokenAllowance,request,poolIndex] = await Promise.all([
    uint(d.reserve,'principalOf(address)',[user]),uint(d.reserve,'totalPrincipal()'),uint(d.reserve,'totalAssets()'),uint(d.reserve,'availableYield()'),
    uint(d.kUSDC,'balanceOf(address)',[user]),uint(d.kUSDC,'balanceOf(address)',[d.treasury]),
    uint(d.kUSDC,'allowance(address,address)',[user,d.reserve]),withdrawal(d,user),uint(d.pool,'liquidityIndex()')
  ]);
  return {principal,totalPrincipal,assets,yieldAvailable,userBalance,treasuryBalance,tokenAllowance,request,poolIndex};
}

async function deploy(accounts) {
  assert(!existsSync(manifestPath), `Already deployed: ${manifestPath}`);
  const payment = json(paymentPath);
  assert(payment.chainId === expectedChain && payment.providerMode === 'fixed-single' && payment.paymentMode === 'token-allowance' && payment.abiVersions?.TokenSwoopPayGBatch === 5 && payment.settlementMode === 'bill-total', 'Deploy bill-total PayG first');
  const owner = decodedAddress(await call(payment.kUSDC,'owner()'));
  assert(owner === accounts.accounts.deployer.address.toLowerCase(), 'Demo deployer must own kUSDC');
  const lock = json('dependencies.lock.json');
  const names = ['TokenSwoopReserve','TestnetAavePool','TestnetAToken'];
  if (!args.includes('--resume')) execFileSync('.toolchain/bin/forge',['build','--skip','test'],{stdio:['ignore','pipe','pipe']});
  for (const name of names) {
    const artifact = json(`out/${name === 'TokenSwoopReserve' ? 'TokenSwoopReserve' : 'TestnetAavePool'}.sol/${name}.json`);
    assert(artifact.metadata.compiler.version === `${lock.solidity.version}+commit.${lock.solidity.commit}`, 'Compiler differs from dependency lock');
  }
  const wallet = accounts.accounts.deployer;
  const progress = option('deployment-journal', `reports/${prefix}-reserve-deployment-progress.json`);
  if (!args.includes('--resume')) assert(!existsSync(progress), 'Deployment was already attempted; inspect its broadcast and use --resume');
  save(progress,{chainId:expectedChain,kUSDC:payment.kUSDC,owner,stage:'broadcasting',sourceSubmitted:false});
  execFileSync('.toolchain/bin/forge',['script',`script/DeployReserve.s.sol:DeployReserve${local ? 'Local' : 'Testnet'}`,
    '--rpc-url',rpcUrl,'--broadcast','--slow',...(args.includes('--resume') ? ['--resume'] : []),
    '--keystore',wallet.keystore,'--password-file',accounts.passwordFile,'--sender',wallet.address],
    {encoding:'utf8',stdio:['ignore','pipe','pipe'],maxBuffer:10_000_000,
      env:{...process.env,DEPLOYER_ADDRESS:wallet.address,PAYMENT_TOKEN_ADDRESS:payment.kUSDC,SETTLEMENT_OPERATOR_ADDRESS:accounts.accounts.provider.address}});
  const broadcast = json(`broadcast/DeployReserve.s.sol/${expectedChain}/run-latest.json`);
  const creates = broadcast.transactions.filter(t => t.transactionType === 'CREATE');
  const pool = address(creates.find(t => t.contractName === 'TestnetAavePool')?.contractAddress);
  const reserve = address(creates.find(t => t.contractName === 'TokenSwoopReserve')?.contractAddress);
  for (const receipt of broadcast.receipts) await confirmed(receipt);
  const creationReceipt = contract => broadcast.receipts.find(r => r.contractAddress?.toLowerCase() === contract.toLowerCase());
  const poolReceipt = creationReceipt(pool), reserveReceipt = creationReceipt(reserve);
  assert(poolReceipt && reserveReceipt, 'No finalized creation receipt for both contracts');
  const aToken = decodedAddress(await call(pool,'receiptToken()'));
  for (const contract of [pool,reserve,aToken]) assert(await rpc('eth_getCode',[contract,'latest']) !== '0x', 'Deployed code missing');
  assert(await uint(reserve,'totalPrincipal()') === 0n && await uint(reserve,'totalAssets()') === 1_000_000n, 'Platform buffer was not initialized');
  const d = {chainId:expectedChain,rpc:rpcUrl,kUSDC:payment.kUSDC,paygBatch:payment.paygBatch,pool,aToken,reserve,
    owner:wallet.address,settlementOperator:accounts.accounts.provider.address,treasury:wallet.address,
    compiler:lock.solidity,testnetMock:true,sourceVerified:false,withdrawalMode:'gateway-clearance',
    tierManagement:'provider',paymentProvider:payment.provider,providerMode:payment.providerMode,paymentMode:payment.paymentMode,
    platformBuffer:'1000000',yieldAllocation:'platform-treasury',abiVersion:2,createdAt:new Date().toISOString(),
    creationTransactions:{TestnetAavePool:poolReceipt.transactionHash,TokenSwoopReserve:reserveReceipt.transactionHash,TestnetAToken:poolReceipt.transactionHash},
    deploymentTransactions:broadcast.receipts.map(r => ({hash:r.transactionHash,block:r.blockNumber,gasUsed:r.gasUsed}))};
  save(manifestPath,d);
  save(progress,{...d,stage:'complete'});
  await exportAbi();
  console.log(`Reserve deployed: ${reserve}. TEST MOCK pool: ${pool}. Source was not uploaded.`);
}

async function exportAbi() {
  for (const name of ['TokenSwoopReserve','TestnetAavePool','TestnetAToken']) {
    const artifact = json(`out/${name === 'TokenSwoopReserve' ? 'TokenSwoopReserve' : 'TestnetAavePool'}.sol/${name}.json`);
    if (name === 'TokenSwoopReserve') assert(!artifact.abi.some(item => /tier|discount/i.test(item.name ?? '')), 'Reserve ABI must not contain provider tier policy');
    save(`abi/${name}.json`,artifact.abi);
  }
}

async function verify(d) {
  const block = await rpc('eth_getBlockByNumber',[local ? 'latest' : 'finalized',false]);
  const reports = {};
  const normalized = (code,refs) => {
    const bytes = Buffer.from(code.slice(2),'hex');
    for (const slots of Object.values(refs)) for (const {start,length} of slots) bytes.fill(0,start,start+length);
    return bytes.toString('hex');
  };
  const word = value => address(value).slice(2).toLowerCase().padStart(64,'0');
  for (const [name,contract,constructor] of [
    ['TestnetAavePool',d.pool,[d.kUSDC,d.owner]],['TokenSwoopReserve',d.reserve,[d.kUSDC,d.pool,d.owner,d.settlementOperator,d.treasury]],
    ['TestnetAToken',d.aToken,[d.pool,d.kUSDC]]]) {
    const artifact = json(`out/${name === 'TokenSwoopReserve' ? 'TokenSwoopReserve' : 'TestnetAavePool'}.sol/${name}.json`);
    assert(artifact.metadata.compiler.version === `${d.compiler.version}+commit.${d.compiler.commit}`, 'Compiler mismatch');
    const code = await rpc('eth_getCode',[contract,block.number]);
    assert(code !== '0x' && normalized(code,artifact.deployedBytecode.immutableReferences) === normalized(artifact.deployedBytecode.object,artifact.deployedBytecode.immutableReferences), `${name} runtime mismatch`);
    const tx = await rpc('eth_getTransactionByHash',[d.creationTransactions[name]]);
    const receipt = await rpc('eth_getTransactionReceipt',[d.creationTransactions[name]]);
    assert(receipt && BigInt(receipt.status) === 1n && BigInt(receipt.blockNumber) <= BigInt(block.number), 'Creation not finalized');
    if (name !== 'TestnetAToken') {
      assert(receipt.contractAddress?.toLowerCase() === contract.toLowerCase(), `${name} creation address mismatch`);
      assert(tx.to === null && tx.input.toLowerCase() === artifact.bytecode.object.toLowerCase() + constructor.map(word).join(''), `${name} creation input mismatch`);
    } else {
      assert(decodedAddress(await call(d.pool,'receiptToken()',[],undefined,block.number)) === d.aToken.toLowerCase(), 'Nested receipt token mismatch');
      assert(decodedAddress(await call(d.aToken,'POOL()',[],undefined,block.number)) === d.pool.toLowerCase(), 'Receipt token pool mismatch');
      assert(decodedAddress(await call(d.aToken,'UNDERLYING_ASSET_ADDRESS()',[],undefined,block.number)) === d.kUSDC.toLowerCase(), 'Receipt token asset mismatch');
    }
    reports[name] = {address:contract,compiler:artifact.metadata.compiler.version,runtimeBytes:(code.length-2)/2,
      runtimeMatches:true,creationChecked:name === 'TestnetAToken' ? 'Created inside the verified pool constructor; runtime and immutable bindings checked' : true,
      creationTransaction:receipt.transactionHash};
  }
  for (const signature of ['reserveToken()','aavePool()','aToken()','treasury()']) {
    const expected = {'reserveToken()':d.kUSDC,'aavePool()':d.pool,'aToken()':d.aToken,'treasury()':d.treasury}[signature];
    assert(decodedAddress(await call(d.reserve,signature,[],undefined,block.number)) === expected.toLowerCase(), `Unexpected ${signature}`);
  }
  const assets = BigInt(await call(d.reserve,'totalAssets()',[],undefined,block.number));
  const principal = BigInt(await call(d.reserve,'totalPrincipal()',[],undefined,block.number));
  assert(assets >= principal,'Principal coverage failure');
  const report = {chainId:expectedChain,finalizedBlock:BigInt(block.number).toString(),checkedAt:new Date().toISOString(),
    contracts:reports,assets,principal,sourceSubmitted:false,testnetMock:true,abiVersion:d.abiVersion,tierManagement:d.tierManagement,mainnetTransactionsBroadcast:false};
  save(`reports/${prefix}-reserve-verification.json`,report);
  console.log(`Reserve artifacts and principal backing verified at block ${report.finalizedBlock}.`);
}

async function expectRevert(to, signature, values, from) {
  try { await call(to,signature,values,from); }
  catch (error) {
    assert(error.rpcError && /revert/i.test(error.message), `Unexpected RPC failure: ${error.message}`);
    return {method:'eth_call',broadcast:false,message:error.message,data:error.rpcError.data};
  }
  throw new Error(`${signature} unexpectedly succeeded`);
}

async function demo(d, accounts) {
  const user = accounts.accounts.user.address, provider = accounts.accounts.provider.address;
  let p = existsSync(progressPath) ? json(progressPath) : {reserve:d.reserve,paygBatch:d.paygBatch,chainId:expectedChain,completed:[],receipts:{},checks:{},
    billId:cast('keccak',`reserve-demo:${randomUUID()}`),gatewayDemo:true,productionGatewayImplemented:false};
  assert(p.reserve.toLowerCase() === d.reserve.toLowerCase() && p.chainId === expectedChain && p.paygBatch?.toLowerCase() === d.paygBatch.toLowerCase(),'Demo deployment or payment contract differs from journal; finish or archive the previous run before starting a new demo');
  save(progressPath,p);
  const persist = () => save(progressPath,p);
  const step = async (name,fn) => {
    if (p.completed.includes(name)) return;
    if (p.receipts[name]) await confirmed(p.receipts[name]);
    else await fn(receipt => { p.receipts[name] = receipt; persist(); });
    p.completed.push(name); persist();
  };
  const tx = (role,to,sig,values,receipt) => send(accounts,role,to,sig,values,receipt);
  await step('mint-user-test-tokens',async receipt => {
    const balance = await uint(d.kUSDC,'balanceOf(address)',[user]);
    if (balance < 1250_000_000n) await tx('deployer',d.kUSDC,'mint(address,uint256)',[user,1250_000_000n-balance],receipt);
  });
  await step('mint-mock-yield-funding',async receipt => {
    const balance = await uint(d.kUSDC,'balanceOf(address)',[d.owner]);
    if (balance < 10_000_000n) await tx('deployer',d.kUSDC,'mint(address,uint256)',[d.owner,10_000_000n-balance],receipt);
  });
  if (!p.initial) { p.initial = await snapshot(d,accounts); persist(); }
  await step('approve-reserve',receipt => tx('user',d.kUSDC,'approve(address,uint256)',[d.reserve,1000_000_000n],receipt));
  for (const [name,increment,target] of [['first',50_000_000n,50_000_000n],['second',150_000_000n,200_000_000n],['third',800_000_000n,1000_000_000n]]) {
    await step(`deposit-${name}`,async receipt => {
      const principal = await uint(d.reserve,'principalOf(address)',[user]);
      if (principal !== target) { assert(principal === target-increment,'Unexpected principal during deposit retry'); await tx('user',d.reserve,'deposit(uint256)',[increment],receipt); }
      const state = await snapshot(d,accounts); assert(state.principal === target,'Cumulative deposit principal mismatch');
      p.checks[name] = state; persist();
    });
  }
  await step('approve-payg',receipt => tx('user',d.kUSDC,'approve(address,uint256)',[d.paygBatch,10_000_000n],receipt));
  const request = async (name,value) => step(name,async receipt => {
    const existing = await withdrawal(d,user);
    const key = `${name}Id`;
    if (!p[key]) { p[key] = (existing.requestId+1n).toString(); persist(); }
    if (!existing.active || existing.requestId !== BigInt(p[key])) await tx('user',d.reserve,'requestWithdrawal(uint256)',[value],receipt);
    assert((await withdrawal(d,user)).requestId === BigInt(p[key]),'Withdrawal request differs');
  });
  await request('partial-request',400_000_000n);
  await step('unsettled-fees-block-withdrawal',async () => {
    assert((await withdrawal(d,user)).active,'Gateway must freeze new API work');
    p.checks.unsettledFees = await expectRevert(d.reserve,'withdraw(uint256)',[p['partial-requestId']],user);
  });
  await step('settle-old-bill',async receipt => {
    if (await uint(d.paygBatch,'isCharged(address,bytes32)',[user,p.billId]) === 0n) {
      await tx('provider',d.paygBatch,'chargeBatch(address,bytes32,uint256)',[user,p.billId,12600],receipt);
    }
    assert(await uint(d.reserve,'principalOf(address)',[user]) === 1000_000_000n,'PayG touched reserve principal');
  });
  await step('approve-mock-yield',receipt => tx('deployer',d.kUSDC,'approve(address,uint256)',[d.pool,10_000_000n],receipt));
  await step('fund-mock-yield',async receipt => {
    if (!p.yieldTargetIndex) {
      const index = await uint(d.pool,'liquidityIndex()');
      const scaled = await uint(d.aToken,'scaledTotalSupply()');
      p.yieldBeforeIndex = index.toString(); p.yieldTargetIndex = (index + 10_000_000n * 10n**27n / scaled).toString(); persist();
    }
    const index = await uint(d.pool,'liquidityIndex()');
    if (index !== BigInt(p.yieldTargetIndex)) { assert(index === BigInt(p.yieldBeforeIndex),'Unexpected mock index'); await tx('deployer',d.pool,'accrueYield(uint256)',[10_000_000n],receipt); }
  });
  await step('harvest-platform-yield',async receipt => {
    if (!p.harvestAmount) {
      p.harvestAmount = (await uint(d.reserve,'availableYield()')).toString();
      p.harvestBalanceBefore = (await uint(d.kUSDC,'balanceOf(address)',[d.treasury])).toString(); persist();
    }
    const current = await uint(d.kUSDC,'balanceOf(address)',[d.treasury]);
    if (current !== BigInt(p.harvestBalanceBefore)+BigInt(p.harvestAmount)) await tx('deployer',d.reserve,'withdrawYield(uint256)',[p.harvestAmount],receipt);
    assert(await uint(d.reserve,'principalOf(address)',[user]) === 1000_000_000n,'Yield harvest touched principal');
  });
  await step('simulate-illiquidity',receipt => tx('deployer',d.pool,'setWithdrawalLimit(uint256)',[0],receipt));
  const approve = async (name,id) => step(name,async receipt => {
    assert(await uint(d.paygBatch,'isCharged(address,bytes32)',[user,p.billId]) === 1n,'Demo bill not settled');
    const current = await withdrawal(d,user);
    if (current.approvedUntil === 0n) await tx('provider',d.reserve,'approveWithdrawal(address,uint256,uint64)',[user,id,Math.floor(Date.now()/1000)+3600],receipt);
  });
  await approve('approve-partial-withdrawal',p['partial-requestId']);
  await step('liquidity-failure-keeps-request',async () => {
    const before = await snapshot(d,accounts);
    p.checks.liquidityFailure = await expectRevert(d.reserve,'withdraw(uint256)',[p['partial-requestId']],user);
    const after = await snapshot(d,accounts);
    assert(JSON.stringify(before,(_,v)=>typeof v==='bigint'?v.toString():v) === JSON.stringify(after,(_,v)=>typeof v==='bigint'?v.toString():v),'Failed withdrawal changed state');
  });
  await step('restore-liquidity',receipt => tx('deployer',d.pool,'setWithdrawalLimit(uint256)',[2n**256n-1n],receipt));
  await step('withdraw-partial-original-request',async receipt => {
    if ((await withdrawal(d,user)).active) await tx('user',d.reserve,'withdraw(uint256)',[p['partial-requestId']],receipt);
    p.checks.partial = await snapshot(d,accounts);
    assert(p.checks.partial.principal === 600_000_000n,'Partial withdrawal principal mismatch');
  });
  await request('full-request',600_000_000n);
  await approve('approve-full-withdrawal',p['full-requestId']);
  await step('withdraw-rest',async receipt => {
    if ((await withdrawal(d,user)).active) await tx('user',d.reserve,'withdraw(uint256)',[p['full-requestId']],receipt);
    assert(await uint(d.reserve,'principalOf(address)',[user]) === 0n,'Principal not fully returned');
  });
  await step('revoke-payg-token-allowance',receipt => tx('user',d.kUSDC,'approve(address,uint256)',[d.paygBatch,0],receipt));
  assert(await uint(d.kUSDC,'allowance(address,address)',[user,d.paygBatch]) === 0n,'PayG token allowance not revoked');
  p.final = await snapshot(d,accounts); persist();
  assert(p.final.userBalance === BigInt(p.initial.userBalance)-12600n && p.final.principal === 0n && !p.final.request.active,'Final reserve/payment conservation failed');
  const report = {...p,testnetMock:true,abiVersion:d.abiVersion,tierManagement:d.tierManagement,yieldFundedByTestTokens:true,apiFreezeDemonstratedByScript:true,
    note:'Withdrawal failure checks are eth_call simulations; state rollback and real pool failure/retry are also covered by unit/invariant/fork tests.',
    sourceSubmitted:false,completedAt:new Date().toISOString()};
  save(`reports/${prefix}-reserve-demo.json`,report);
  console.log(`Reserve demo passed: principal 50 → 200 → 1000 → 600 → 0 kUSDC; bill paid 0.012600; harvested ${p.harvestAmount} units. Report: reports/${prefix}-reserve-demo.json`);
}

async function main() {
  if (command === 'help') { console.log('Commands: deploy [--resume], export-abi, verify, status, demo, approve --amount 1000, deposit --amount 50, fund-buffer --amount 1, request-withdrawal --amount 50, approve-withdrawal --request-id N --fees-settled --inflight-drained [--expires-in 3600], withdraw --request-id N, cancel-withdrawal, withdraw-yield --amount 1. Default Monad Testnet 10143; --local requires loopback chain 31337.'); return; }
  if (command === 'export-abi') { await exportAbi(); console.log('Reserve ABI exported locally.'); return; }
  await checkChain();
  const accounts = wallets();
  if (command === 'deploy') return deploy(accounts);
  const d = deployment();
  if (command === 'verify') return verify(d);
  if (command === 'status') { console.log(JSON.stringify({deployment:d,state:await snapshot(d,accounts)},(_,v)=>typeof v==='bigint'?v.toString():v,2)); return; }
  if (command === 'demo') {
    const payment = json(paymentPath);
    assert(payment.providerMode === 'fixed-single' && payment.paymentMode === 'token-allowance' && payment.abiVersions?.TokenSwoopPayGBatch === 5 && payment.provider.toLowerCase() === accounts.accounts.provider.address.toLowerCase() && d.paygBatch.toLowerCase() === payment.paygBatch.toLowerCase(), 'Reserve demo requires the current fixed Provider and Batch deployment');
    return demo(d,accounts);
  }
  const requestId = () => integer(option('request-id'),'request-id');
  let result;
  if (command === 'approve') result = await send(accounts,'user',d.kUSDC,'approve(address,uint256)',[d.reserve,amount(option('amount'))]);
  else if (command === 'deposit') result = await send(accounts,'user',d.reserve,'deposit(uint256)',[positive(option('amount'))]);
  else if (command === 'fund-buffer') result = await send(accounts,'deployer',d.reserve,'fundBuffer(uint256)',[positive(option('amount'))]);
  else if (command === 'request-withdrawal') result = await send(accounts,'user',d.reserve,'requestWithdrawal(uint256)',[positive(option('amount'))]);
  else if (command === 'cancel-withdrawal') result = await send(accounts,'user',d.reserve,'cancelWithdrawal()',[]);
  else if (command === 'withdraw') result = await send(accounts,'user',d.reserve,'withdraw(uint256)',[requestId()]);
  else if (command === 'withdraw-yield') result = await send(accounts,'deployer',d.reserve,'withdrawYield(uint256)',[positive(option('amount'))]);
  else if (command === 'approve-withdrawal') {
    assert(args.includes('--fees-settled') && args.includes('--inflight-drained'),'The gateway must confirm ALL fees settled and in-flight work drained before approving');
    const duration = integer(option('expires-in','3600'),'expires-in');
    assert(duration > 0n && duration < 86400n * 30n,'Use a positive approval lifetime under 30 days');
    result = await send(accounts,'provider',d.reserve,'approveWithdrawal(address,uint256,uint64)',[accounts.accounts.user.address,requestId(),BigInt(Math.floor(Date.now()/1000))+duration]);
  } else throw new Error('Unknown command; run help');
  console.log(`Confirmed: ${result.transactionHash}`);
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
