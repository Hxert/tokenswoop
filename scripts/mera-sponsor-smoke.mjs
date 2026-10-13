// Real chain transactions, real Mera SDK signatures, isolated backend database.
// PRF entropy is a private QA fixture; this is not a physical authenticator test.
import fs from 'node:fs'
import path from 'node:path'
import { randomBytes, randomUUID } from 'node:crypto'
import { spawn, execFileSync } from 'node:child_process'
import { once } from 'node:events'
import { readEnvironment, networkValue } from './lib/network-env.mjs'
import { encodeFunctionData, decodeFunctionResult, parseAbi, keccak256 } from '../frontend/node_modules/viem/_esm/index.js'
import { withPrfAccount } from '../frontend/src/meraWallet.ts'
import { meraTypedData } from '../frontend/src/meraSponsorship.ts'

const root=path.resolve(import.meta.dirname,'..'),network=process.argv[2]||'testnet'
if(!['testnet','mainnet'].includes(network))throw new Error('Select testnet or mainnet')
const run=process.argv[3]||''
if(run&&!/^[a-z0-9-]{1,32}$/.test(run))throw new Error('Invalid QA run name')
const env=readEnvironment(root),chainID=network==='mainnet'?143:10143,prefix=network.toUpperCase()+'_'
const dir=path.join(root,'.local/qa',`mera-${network}${run?'-'+run:''}`),file=path.join(dir,'journal.json'),reportFile=path.join(dir,'report.json')
fs.mkdirSync(dir,{recursive:true})
const j=fs.existsSync(file)?JSON.parse(fs.readFileSync(file)):{entropy:randomBytes(32).toString('hex'),operations:{}}
const report=fs.existsSync(reportFile)?JSON.parse(fs.readFileSync(reportFile)):{chainID,checks:[],transactions:{}}
const persist=()=>{fs.writeFileSync(file+'.tmp',JSON.stringify(j,null,2),{mode:0o600});fs.renameSync(file+'.tmp',file);fs.writeFileSync(reportFile,JSON.stringify(report,null,2)+'\n')}
const check=(name,ok)=>{if(!ok)throw new Error('Failed: '+name);if(!report.checks.includes(name))report.checks.push(name);persist();console.log('PASS',name)}
const sleep=ms=>new Promise(r=>setTimeout(r,ms))
const withAccount=fn=>withPrfAccount(Uint8Array.from(Buffer.from(j.entropy,'hex')),fn)
const wallet=await withAccount(a=>a.address.toLowerCase());report.wallet=wallet;persist()
const manifest=JSON.parse(fs.readFileSync(path.join(root,`contracts/deployments/mera-${network}.json`)))
const sponsorMode=networkValue(env,network,'MERA_SPONSOR_MODE')||'dedicated'
manifest.sponsor=networkValue(env,network,sponsorMode==='platform'?'PAYMENT_PROVIDER':'MERA_SPONSOR_ADDRESS')
if(report.sponsor&&report.sponsor.toLowerCase()!==manifest.sponsor.toLowerCase())throw new Error('QA run sponsor changed; use a new run name after recovering previous transactions')
report.sponsor=manifest.sponsor;report.sponsorMode=sponsorMode;persist()
const rpcURL=networkValue(env,network,'PAYMENT_RPC_URL')
async function rpc(method,params){try{const r=await fetch(rpcURL,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)}).then(r=>r.json());if(r.error)throw 0;return r.result}catch{throw new Error('RPC request failed: '+method)}}
const abi=parseAbi(['function allowance(address,address) view returns(uint256)','function balanceOf(address) view returns(uint256)','function transfer(address,uint256) returns(bool)','function principalOf(address) view returns(uint256)'])
const read=async(to,fn,args)=>decodeFunctionResult({abi,functionName:fn,data:await rpc('eth_call',[{to,data:encodeFunctionData({abi,functionName:fn,args})},'latest'])})
const children=[]
let cookie='',csrf=''
const base='http://127.0.0.1:8081'
async function api(route,body){
 const r=await fetch(base+route,{method:body===undefined?'GET':'POST',headers:{'content-type':'application/json',origin:'http://localhost:5173',cookie,'x-csrf-token':csrf},...(body===undefined?{}:{body:JSON.stringify(body)}),signal:AbortSignal.timeout(45000)})
 if(route==='/api/auth/login'){
  const parts=r.headers.getSetCookie().map(s=>s.split(';')[0]);cookie=parts.join('; ');csrf=parts.find(s=>s.startsWith('ts_csrf='))?.slice(8)||''
 }
 const data=await r.json();if(!r.ok)throw new Error(`API ${route}: ${r.status} ${data.error?.code||''}`);return data
}
async function fundToken(){
 if(network==='mainnet')return // Mainnet smoke revokes zero allowance; no USDC is transferred.
 const wallets=JSON.parse(fs.readFileSync(path.join(root,'contracts/deployments/testnet-wallets.json'))),user=wallets.accounts.user
 if(!j.funding){
  const data=encodeFunctionData({abi,functionName:'transfer',args:[wallet,10000n]})
  const nonce=await rpc('eth_getTransactionCount',[user.address,'pending']);if(nonce!==await rpc('eth_getTransactionCount',[user.address,'latest']))throw new Error('Funding wallet has pending transaction')
  const gas=BigInt(await rpc('eth_estimateGas',[{from:user.address,to:manifest.token,data}]))*125n/100n,price=BigInt(await rpc('eth_gasPrice',[]))*2n
  if(gas*price>50000000000000000n)throw new Error('Funding gas cap exceeded')
  let raw;try{raw=execFileSync(path.join(root,'contracts/.toolchain/bin/cast'),['mktx',manifest.token,data,'--legacy','--chain',String(chainID),'--nonce',BigInt(nonce).toString(),'--gas-limit',gas.toString(),'--gas-price',price.toString(),'--keystore',path.resolve(root,'contracts',user.keystore),'--password-file',path.resolve(root,'contracts',wallets.passwordFile),'--from',user.address],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()}catch{throw new Error('Funding signer failed')}
  j.funding={raw,hash:keccak256(raw)};persist()
 }
 for(let i=0;i<50;i++){
  const r=await rpc('eth_getTransactionReceipt',[j.funding.hash]);if(r){check('Test token funding confirmed',r.status==='0x1');return}
  if(i%4===0)try{await rpc('eth_sendRawTransaction',[j.funding.raw])}catch{}
  await sleep(800)
 }
 throw new Error('Funding pending; rerun to resume')
}
async function operation(name,action,amount){
 const op=j.operations[name]??={id:randomUUID()};persist()
 let o=await api('/api/wallet/mera/transactions',{id:op.id,action,amount_units:String(amount)})
 if(o.state==='prepared'){
  check(name+': wallet and chain unchanged',o.wallet===wallet&&o.chain_id===chainID&&o.implementation.toLowerCase()===manifest.implementation.toLowerCase())
  const signed=await withAccount(async a=>{
   const auth=o.authorization_nonce===null?null:await a.signAuthorization({address:manifest.implementation,chainId:chainID,nonce:o.authorization_nonce})
   return {signature:await a.signTypedData(meraTypedData(o)),authorization:auth&&{address:auth.address,chainId:auth.chainId,nonce:auth.nonce,r:auth.r,s:auth.s,yParity:auth.yParity}}
  })
  o=await api(`/api/wallet/mera/transactions/${op.id}/submit`,signed)
  const same=await api(`/api/wallet/mera/transactions/${op.id}/submit`,signed)
  check(name+': repeated submission keeps operation ID',same.operation_id===o.operation_id)
 }
 for(let i=0;i<75&&!['confirmed','reverted','rejected','review','expired'].includes(o.state);i++){
  await sleep(1200);o=await api(`/api/wallet/mera/transactions/${op.id}`)
 }
 check(name+': finalized successfully',o.state==='confirmed')
 const tx=await rpc('eth_getTransactionByHash',[o.hash])
 check(name+': sponsor paid gas',tx.from.toLowerCase()===manifest.sponsor.toLowerCase()&&tx.to.toLowerCase()===wallet)
 if(name==='api-approve'||network==='mainnet')check('Initial operation used EIP-7702 type 4',tx.type==='0x4')
 report.transactions[name]={id:o.id,hash:o.hash,type:tx.type};persist()
 console.log('CONFIRMED',name,o.hash)
}
async function main(){
 if(report.completed){console.log('This network smoke test is already complete; no new transactions sent.');return}
 let live
 try{live=await fetch('http://127.0.0.1:8080/api/config',{signal:AbortSignal.timeout(2000)}).then(r=>r.json())}catch{}
 if(live?.chain_id===chainID&&(live?.mera?.gas_sponsorship||sponsorMode==='platform'))throw new Error('The live backend uses this sponsor. Stop its API and worker before running an isolated QA worker.')
 check('Correct RPC network',BigInt(await rpc('eth_chainId',[]))===BigInt(chainID))
 check('User has no MON before sponsorship',BigInt(await rpc('eth_getBalance',[wallet,'latest']))===0n)
 // Keep the isolated database on failure so an ambiguous operation can be resumed.
 const database='tokenswoop_mera_'+network+'_qa'+(run?'_'+run.replaceAll('-','_'):''),pg='/opt/homebrew/opt/postgresql@18/bin',dsn=new URL(networkValue(env,network,'DATABASE_URL'))
 const pgEnv={...process.env,PGHOST:dsn.hostname,PGPORT:dsn.port||'5432',PGUSER:decodeURIComponent(dsn.username),PGPASSWORD:decodeURIComponent(dsn.password),PGDATABASE:'postgres'}
 const exists=execFileSync(path.join(pg,'psql'),['-tAc',`SELECT 1 FROM pg_database WHERE datname='${database}'`],{env:pgEnv,encoding:'utf8'}).trim()
 if(!exists)execFileSync(path.join(pg,'createdb'),[database],{env:pgEnv,stdio:'pipe'})
 dsn.pathname='/'+database;env[prefix+'DATABASE_URL']=dsn.toString();env.CHAIN_NETWORK=network;env.CHAIN_ID=String(chainID);env.LISTEN_ADDR='127.0.0.1:8081';env.PUBLIC_ORIGIN='http://localhost:5173';env.APP_ENV='test';env.PRIVY_ENABLED='false'
 env.GOCACHE=path.join(root,'backend/.cache/go-build');env.GOMODCACHE=path.join(root,'backend/.cache/gomod');env.GOPATH=path.join(root,'backend/.cache/gopath')
 for(const service of ['api','worker']){
  const bin=path.join(dir,service)
  execFileSync('go',['build','-o',bin,`./cmd/${service}`],{cwd:path.join(root,'backend'),env,stdio:'pipe',timeout:60000})
  const out=fs.openSync(path.join(dir,service+'.log'),'a',0o600)
  children.push(spawn(bin,[],{cwd:root,env,stdio:['ignore',out,out]}));fs.closeSync(out)
 }
 for(let i=0;i<60;i++){
  try{const r=await fetch(base+'/health/ready');if(r.ok)break}catch{}
  if(children.some(c=>c.exitCode!==null))throw new Error('QA backend failed; inspect private service logs')
  await sleep(700)
 }
 const challenge=await api('/api/auth/challenge',{wallet,source:'mera'})
 const signature=await withAccount(a=>a.signMessage({message:challenge.message}))
 await api('/api/auth/login',{id:challenge.id,signature})
 await fundToken()
 if(network==='testnet'){
  await operation('api-approve','payment_approve',10000n)
  await operation('reserve-approve','reserve_approve',10000n)
  await operation('reserve-deposit','reserve_deposit',10000n)
  if(!report.transactions['reserve-withdraw'])check('Original wallet owns reserve principal',await read(manifest.reserve,'principalOf',[wallet])===10000n)
  if(!report.transactions['api-revoke'])check('Reserve deposit preserves payment allowance',await read(manifest.token,'allowance',[wallet,manifest.payment])===10000n)
  await operation('reserve-withdraw','reserve_withdraw',10000n)
  check('Full principal returned to original wallet',await read(manifest.reserve,'principalOf',[wallet])===0n&&await read(manifest.token,'balanceOf',[wallet])===10000n)
 }
 await operation('api-revoke','payment_approve',0n)
 check('Payment allowance revoked',await read(manifest.token,'allowance',[wallet,manifest.payment])===0n)
 check('User still has zero MON',BigInt(await rpc('eth_getBalance',[wallet,'latest']))===0n)
 check('Original address has expected delegation',(await rpc('eth_getCode',[wallet,'latest'])).toLowerCase()==='0xef0100'+manifest.implementation.slice(2).toLowerCase())
 const fresh=await api('/api/auth/challenge',{wallet,source:'mera'});await api('/api/auth/login',{id:fresh.id,signature:await withAccount(a=>a.signMessage({message:fresh.message}))})
 check('Login still resolves original wallet after delegation',(await api('/api/me')).user.wallet===wallet)
 report.completed=true;persist()
}
try{await main()}catch(e){console.error(e.message);process.exitCode=1}finally{await Promise.all(children.map(async c=>{if(c.exitCode!==null)return;const done=once(c,'exit');c.kill('SIGTERM');await done}))}
