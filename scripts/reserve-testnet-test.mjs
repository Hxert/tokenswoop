import {testnetEnvironment} from './lib/network-env.mjs'
// Resumable test-wallet-only exercise of the real API and deployed Reserve.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import {execFileSync} from 'node:child_process'
const root=path.resolve(import.meta.dirname,'..'),contracts=path.join(root,'contracts')
Object.assign(process.env, testnetEnvironment(root), {CHAIN_ID:'10143'})
const read=p=>JSON.parse(fs.readFileSync(p,'utf8'))
const d=read(path.join(contracts,'deployments/monad-testnet-reserve.json')),w=read(path.join(contracts,'deployments/testnet-wallets.json'))
const user=w.accounts.user,castPath=path.join(contracts,'.toolchain/bin/cast')
assert.equal(d.chainId,10143);assert.equal(process.env.CHAIN_ID,'10143');assert.equal(process.env.RESERVE_ENABLED,'true');assert.equal(d.reserve.toLowerCase(),process.env.RESERVE_CONTRACT)
const run=process.argv[2]??'reserve_v1';assert(/^[a-z0-9_]{1,40}$/.test(run))
const dir=path.join(contracts,'reports/reserve-workspace',run);fs.mkdirSync(dir,{recursive:true,mode:0o700})
const journalPath=path.join(dir,'journal.json'), reportPath=path.join(dir,'report.json'),lockPath=path.join(dir,'.lock')
fs.writeFileSync(lockPath,String(process.pid),{flag:'wx',mode:0o600})
const save=(file,v)=>{fs.writeFileSync(file+'.tmp',JSON.stringify(v,null,2)+'\n',{mode:0o600});fs.renameSync(file+'.tmp',file)}
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))
const cast=(...args)=>{try{return execFileSync(castPath,args.map(String),{cwd:contracts,encoding:'utf8',stdio:['ignore','pipe','pipe'],timeout:45000}).trim()}catch{throw new Error('Test wallet command failed; sensitive output suppressed')}}
async function rpc(method,params){const res=await fetch(process.env.PAYMENT_RPC_URL,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)});assert(res.ok);const v=await res.json();assert(!v.error&&v.result!==undefined,`RPC ${method} failed`);return v.result}
const call=(to,sig,...args)=>rpc('eth_call',[{to,data:cast('calldata',sig,...args)},'finalized'])
let cookie='',csrf=''
async function api(route,body){const res=await fetch('http://127.0.0.1:8080'+route,{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json',Origin:process.env.PUBLIC_ORIGIN,Cookie:cookie,'X-CSRF-Token':csrf},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(30000)});const set=res.headers.getSetCookie();if(set.length){cookie=set.map(v=>v.split(';')[0]).join('; ');csrf=set.find(v=>v.startsWith('ts_csrf='))?.split(';')[0].slice(8)??csrf}const data=res.status===204?null:await res.json();return {status:res.status,data}}
async function confirmed(hash){for(let i=0;i<50;i++){const receipt=await rpc('eth_getTransactionReceipt',[hash]);if(receipt){assert.equal(BigInt(receipt.status),1n,'Test transaction reverted');const final=await rpc('eth_getBlockByNumber',['finalized',false]);if(BigInt(final.number)>=BigInt(receipt.blockNumber)){const canonical=await rpc('eth_getBlockByNumber',[receipt.blockNumber,false]);assert.equal(canonical.hash,receipt.blockHash);return}}await sleep(1000)}throw new Error('Still confirming. Resume the same run.')}
async function transaction(j,name,to,sig,...args){
 let tx=j.transactions[name]
 if(!tx){
  assert.equal(BigInt(await rpc('eth_chainId',[])),10143n)
  const pending=await rpc('eth_getTransactionCount',[user.address,'pending']),latest=await rpc('eth_getTransactionCount',[user.address,'latest']);assert.equal(pending,latest,'Test wallet already has a pending transaction')
  const data=cast('calldata',sig,...args),gas=BigInt(await rpc('eth_estimateGas',[{from:user.address,to,data}]))*120n/100n,price=BigInt(await rpc('eth_gasPrice',[]));assert(gas*price<50_000_000_000_000_000n)
  const raw=cast('mktx',to,data,'--chain','10143','--legacy','--nonce',BigInt(pending),'--gas-limit',gas,'--gas-price',price,'--rpc-url',process.env.PAYMENT_RPC_URL,'--keystore',user.keystore,'--password-file',w.passwordFile,'--from',user.address)
  tx={raw,hash:cast('keccak',raw),to,sig,args:args.map(String)};j.transactions[name]=tx;save(journalPath,j)
 }
 assert.equal(tx.to,to);assert.equal(tx.sig,sig);assert.deepEqual(tx.args,args.map(String))
 if(!await rpc('eth_getTransactionReceipt',[tx.hash])){try{await rpc('eth_sendRawTransaction',[tx.raw])}catch{}}
 await confirmed(tx.hash);console.log(`${name}: finalized ${tx.hash}`)
}
async function status(){const r=await api('/api/reserve/refresh',{});if(r.status===503||r.status===429)return null;assert.equal(r.status,200,JSON.stringify(r.data));return r.data}
async function waitFor(label,predicate){for(let i=0;i<36;i++){const s=await status();if(s&&predicate(s))return s;await sleep(4000)}throw new Error(`Waiting for ${label}; rerun the same run name`)}
async function phase(j,name,fn){if(j.phases.includes(name))return;await fn();j.phases.push(name);save(journalPath,j);console.log(`${name}: passed`)}
try{
 let j=fs.existsSync(journalPath)?read(journalPath):{transactions:{},phases:[],before:{balance:BigInt(await call(d.kUSDC,'balanceOf(address)',user.address)).toString(),principal:BigInt(await call(d.reserve,'principalOf(address)',user.address)).toString(),allowance:BigInt(await call(d.kUSDC,'allowance(address,address)',user.address,d.reserve)).toString()}}
 if(j.complete){console.log(`Already complete: ${reportPath}`)}else{
  assert.equal(j.before.principal,'0');assert.equal(j.before.allowance,'0');assert(BigInt(j.before.balance)>50_000_000n);save(journalPath,j)
  const challenge=await api('/api/auth/challenge',{wallet:user.address});assert.equal(challenge.status,200)
  const signature=cast('wallet','sign',challenge.data.message,'--keystore',user.keystore,'--password-file',w.passwordFile)
  assert.equal((await api('/api/auth/login',{id:challenge.data.id,signature})).status,200)
  await phase(j,'deposit',async()=>{await transaction(j,'approve',d.kUSDC,'approve(address,uint256)',d.reserve,50_000_000);await transaction(j,'deposit',d.reserve,'deposit(uint256)',50_000_000);const s=await waitFor('Builder tier',s=>s.tier.id==='builder'&&s.snapshot.principal_units==='50000000');assert.equal(s.tier.discount_bps,300)})
  await phase(j,'direct-request-freeze-cancel',async()=>{
   await transaction(j,'request-partial',d.reserve,'requestWithdrawal(uint256)',1_000_000)
   const s=await waitFor('withdrawal freeze',s=>s.snapshot.withdrawal.active);j.partialID=s.snapshot.withdrawal.id;save(journalPath,j)
   const r=await api('/api/playground',{model:'codex-auto-review',messages:[{role:'user',content:'Reply OK.'}],max_tokens:1})
   assert.equal(r.status,409,JSON.stringify(r.data));assert.equal(r.data.error.code,'withdrawal_pending')
   await transaction(j,'cancel',d.reserve,'cancelWithdrawal()')
   const restored=await waitFor('cancel finality',s=>!s.snapshot.withdrawal.active);assert.equal(restored.tier.id,'builder');assert.equal(restored.snapshot.principal_units,'50000000')
  })
  await phase(j,'approved-withdrawal',async()=>{
   await transaction(j,'request-all',d.reserve,'requestWithdrawal(uint256)',50_000_000)
   const s=await waitFor('automatic approval',s=>s.snapshot.withdrawal.active && s.snapshot.withdrawal.operator_version===s.snapshot.operator_version && s.snapshot.withdrawal.approved_until>Date.now()/1000+30)
   assert.equal(s.clearance.in_flight,0);assert.equal(s.clearance.usage_unknown,0);assert.equal(s.clearance.open_bills,0);assert.equal(Number(s.clearance.unpaid),0)
   j.fullID=s.snapshot.withdrawal.id;j.approvals=s.approvals;save(journalPath,j)
   await transaction(j,'withdraw',d.reserve,'withdraw(uint256)',j.fullID)
   const final=await waitFor('Free tier',s=>s.snapshot.principal_units==='0' && s.tier.id==='free'&&!s.snapshot.withdrawal.active)
   j.after={balance:final.snapshot.balance_units,principal:final.snapshot.principal_units,allowance:final.snapshot.allowance_units,tier:final.tier.id}
   assert.equal(j.after.balance,j.before.balance);assert.equal(j.after.allowance,'0')
  })
  await api('/api/auth/logout',{})
  j.complete=true;save(journalPath,j)
  save(reportPath,{network:'Monad Testnet',wallet:user.address,reserve:d.reserve,amount:'50 kUSDC',before:j.before,after:j.after,checks:j.phases,transactions:Object.fromEntries(Object.entries(j.transactions).map(([k,v])=>[k,v.hash])),approvals:j.approvals})
  console.log(`Reserve completed; all 50 test kUSDC returned. ${reportPath}`)
 }
}finally{if(cookie){try{await api('/api/auth/logout',{})}catch{}}fs.unlinkSync(lockPath)}
