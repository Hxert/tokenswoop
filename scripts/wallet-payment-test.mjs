import {testnetEnvironment} from './lib/network-env.mjs'
// Opt-in, resumable testnet test of the application settlement service.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFileSync} from 'node:child_process';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..'),contracts=path.join(root,'contracts');
const run=process.argv[2]??'wallet_v1';assert(/^[a-z0-9_]{1,32}$/.test(run),'Use the SAME run name to resume');
Object.assign(process.env, testnetEnvironment(root), {CHAIN_ID:'10143'});
const read=p=>JSON.parse(fs.readFileSync(p,'utf8'));
const current=read(path.join(contracts,'deployments/monad-testnet-reserve-debt.json'));
const d={...current,kUSDC:current.token,paygBatch:current.batch},w=read(path.join(contracts,'deployments/testnet-wallets.json'));
assert(d.chainId===10143&&w.chainId===10143&&process.env.CHAIN_ID==='10143'&&process.env.SETTLEMENT_ENABLED==='true');
assert.equal(process.env.PAYMENT_CONTRACT,d.paygBatch.toLowerCase());
const directory=path.join(contracts,'reports/wallet-payments',run);fs.mkdirSync(directory,{recursive:true,mode:0o700});
const journalPath=path.join(directory,'journal.json'),reportPath=path.join(directory,'report.json');
const lock=path.join(contracts,'reports/wallet-payments/.lock');fs.writeFileSync(lock,String(process.pid),{flag:'wx',mode:0o600});
const save=(p,v)=>{fs.writeFileSync(p+'.tmp',JSON.stringify(v,null,2)+'\n',{mode:0o600});fs.renameSync(p+'.tmp',p)};
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const cast=(...args)=>{try{return execFileSync(path.join(contracts,'.toolchain/bin/cast'),args.map(String),{cwd:contracts,encoding:'utf8',stdio:['ignore','pipe','pipe'],timeout:45000}).trim()}catch{throw new Error('Test wallet command failed; sensitive output suppressed')}};
let last=0;
async function rpc(method,params){await sleep(Math.max(0,last+200-Date.now()));last=Date.now();const res=await fetch(process.env.PAYMENT_RPC_URL,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)});assert(res.ok,'RPC HTTP error');const v=await res.json();assert(!v.error&&v.result!==undefined,`RPC ${method} failed`);return v.result}
const call=(to,signature,...args)=>rpc('eth_call',[{to,data:cast('calldata',signature,...args)},'latest']);
async function snapshot(){return {userBalance:BigInt(await call(d.kUSDC,'balanceOf(address)',w.accounts.user.address)).toString(),providerBalance:BigInt(await call(d.kUSDC,'balanceOf(address)',d.provider)).toString(),allowance:BigInt(await call(d.kUSDC,'allowance(address,address)',w.accounts.user.address,d.paygBatch)).toString(),providerNonce:BigInt(await rpc('eth_getTransactionCount',[d.provider,'latest'])).toString()}}
async function confirmed(hash){for(let i=0;i<40;i++){const receipt=await rpc('eth_getTransactionReceipt',[hash]);if(receipt){assert.equal(BigInt(receipt.status),1n,'Approval reverted');const finalized=await rpc('eth_getBlockByNumber',['finalized',false]);if(BigInt(finalized.number)>=BigInt(receipt.blockNumber)){const canonical=await rpc('eth_getBlockByNumber',[receipt.blockNumber,false]);assert.equal(canonical.hash,receipt.blockHash);return receipt}}await sleep(1000)}throw new Error('Confirmation pending; rerun the same run name')}
async function approve(j,step,units){
 assert.equal(BigInt(await rpc('eth_chainId',[])),10143n);
 let tx=j.transactions[step];
 if(!tx){
  const user=w.accounts.user,nonce=await rpc('eth_getTransactionCount',[user.address,'pending']);assert.equal(BigInt(nonce),BigInt(await rpc('eth_getTransactionCount',[user.address,'latest'])),'User wallet has a pending transaction');
  const data=cast('calldata','approve(address,uint256)',d.paygBatch,units),gas=BigInt(await rpc('eth_estimateGas',[{from:user.address,to:d.kUSDC,data}]))*120n/100n,price=BigInt(await rpc('eth_gasPrice',[]));assert(gas*price<50_000_000_000_000_000n);
  const raw=cast('mktx',d.kUSDC,'approve(address,uint256)',d.paygBatch,units,'--chain','10143','--legacy','--nonce',BigInt(nonce),'--gas-limit',gas,'--gas-price',price,'--rpc-url',process.env.PAYMENT_RPC_URL,'--keystore',user.keystore,'--password-file',w.passwordFile,'--from',user.address);
  tx={raw,hash:cast('keccak',raw),units:String(units)};j.transactions[step]=tx;save(journalPath,j);
 }
 assert.equal(tx.units,String(units));assert.equal(cast('keccak',tx.raw),tx.hash);
 if(!await rpc('eth_getTransactionReceipt',[tx.hash])){try{await rpc('eth_sendRawTransaction',[tx.raw])}catch{/* resolve only this hash */}}
 await confirmed(tx.hash);console.log(`${step}: finalized ${tx.hash}`);
}
function phase(j,name){
 if(j.phases.includes(name))return;
 execFileSync('go',['test','-count=1','-timeout','5m','-run','^TestTestnetWalletBilling$','./internal/httpapi'],{cwd:path.join(root,'backend'),stdio:'inherit',env:{...process.env,TESTNET_PAYMENT_RUN:run,TESTNET_PAYMENT_PHASE:name,TESTNET_CONTRACTS_DIR:contracts,TESTNET_PAYMENT_REPORT:reportPath,GOCACHE:path.join(root,'backend/.cache/go-build'),GOMODCACHE:path.join(root,'backend/.cache/gomod'),GOPATH:path.join(root,'backend/.cache/gopath')}});
 j.phases.push(name);save(journalPath,j);console.log(`${name}: application checks passed`);
}
try{
 assert.equal(BigInt(await rpc('eth_chainId',[])),10143n);
 let j=fs.existsSync(journalPath)?read(journalPath):{run,transactions:{},phases:[],before:await snapshot()};
 if(!fs.existsSync(journalPath)){assert.equal(j.before.allowance,'0','Start with zero test Batch allowance');save(journalPath,j)}
 if(j.complete){const now=await snapshot();assert.deepEqual(now,j.after,'Chain state changed since test completion');console.log(`Already complete; no transactions sent. ${reportPath}`)}else{
  await approve(j,'initial-allowance',120);phase(j,'generate');
  await approve(j,'revoke-before-settlement',0);phase(j,'blocked');
  await approve(j,'restore-allowance',120);phase(j,'settle');
  const after=await snapshot();assert.equal(BigInt(j.before.userBalance)-BigInt(after.userBalance),120n);assert.equal(BigInt(after.providerBalance)-BigInt(j.before.providerBalance),120n);assert.equal(after.allowance,'0');assert.equal(BigInt(after.providerNonce)-BigInt(j.before.providerNonce),1n);
  phase(j,'verify');assert.deepEqual(await snapshot(),after,'Replaying worker changed chain balances or nonce');j.after=after;j.complete=true;save(journalPath,j);
  const report=read(reportPath);save(reportPath,{...report,before:j.before,after,checks:['wallet-only admission','4 completed Chat/Responses JSON/SSE requests','revoked allowance blocks payment','retry preserves bill ID','finalized exact transfer','idempotent worker replay'],authorizationTransactions:Object.fromEntries(Object.entries(j.transactions).map(([k,v])=>[k,v.hash]))});
  console.log(`Paid exactly 0.000120 test kUSDC; repeated settlement sent no duplicate. ${reportPath}`);
 }
}finally{fs.unlinkSync(lock)}
