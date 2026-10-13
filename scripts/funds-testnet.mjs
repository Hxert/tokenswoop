import {testnetEnvironment} from './lib/network-env.mjs'
// Real Monad Testnet checks. Signing journals stay private; reports contain no secrets.
import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { encodeDeployData, encodeFunctionData, decodeFunctionResult, parseAbi, keccak256, toHex } from '../frontend/node_modules/viem/_esm/index.js'
import { testnetRPC } from './lib/testnet-rpc.mjs'

const root=path.resolve(import.meta.dirname,'..'),contracts=path.join(root,'contracts')
const cast=path.join(contracts,'.toolchain/bin/cast'),rpcURL=testnetRPC(root)
const wallets=JSON.parse(fs.readFileSync(path.join(contracts,'deployments/testnet-wallets.json')))
const old=JSON.parse(fs.readFileSync(path.join(contracts,'deployments/monad-testnet-reserve.json')))
const phase=process.argv[2]||'test',dir=path.join(root,'.local/qa/funds-testnet')
fs.mkdirSync(dir,{recursive:true})
const journalPath=path.join(root,'.local/funds-testnet-journal.json')
const journal=fs.existsSync(journalPath)?JSON.parse(fs.readFileSync(journalPath)):{transactions:{}}
const reportPath=path.join(dir,'report.json')
const report=fs.existsSync(reportPath)?JSON.parse(fs.readFileSync(reportPath)):{chain_id:10143,checks:[],transactions:{}}
const persist=()=>{fs.writeFileSync(journalPath,JSON.stringify(journal,null,2),{mode:0o600});fs.chmodSync(journalPath,0o600);fs.writeFileSync(reportPath,JSON.stringify(report,null,2)+'\n')}
const check=(name,ok)=>{if(!ok)throw new Error('Assertion failed: '+name);if(!report.checks.includes(name))report.checks.push(name);persist();console.log('PASS',name)}
const sleep=ms=>new Promise(r=>setTimeout(r,ms))
let last=0
async function rpc(method,params){await sleep(Math.max(0,75-(Date.now()-last)));last=Date.now();let r;try{r=await fetch(rpcURL,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)}).then(r=>r.json())}catch{throw new Error('RPC transport unavailable: '+method)};if(r.error)throw new Error('RPC rejected '+method+' ('+r.error.code+')');return r.result}
const cli=args=>{try{return execFileSync(cast,args,{cwd:contracts,encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()}catch{throw new Error('Local signer failed')}}
function account(role){const a=wallets.accounts[role];return{...a,keystore:path.resolve(contracts,a.keystore)}}
const password=path.resolve(contracts,wallets.passwordFile)
async function send(name,role,to,data='0x',value=0n){
 let j=journal.transactions[name],a=typeof role==='string'?account(role):role
 if(!j){
  const nonce=await rpc('eth_getTransactionCount',[a.address,'pending']),latest=await rpc('eth_getTransactionCount',[a.address,'latest']);if(nonce!==latest)throw new Error('Signer has another pending transaction')
  const gas=BigInt(await rpc('eth_estimateGas',[{from:a.address,...(to?{to}:{}),data,value:toHex(value)}]))*120n/100n
  const price=BigInt(await rpc('eth_gasPrice',[]));if(gas*price>450000000000000000n)throw new Error('Testnet gas cap exceeded')
  const args=['mktx','--legacy','--chain','10143','--nonce',BigInt(nonce).toString(),'--gas-limit',gas.toString(),'--gas-price',price.toString(),'--value',value.toString(),'--keystore',a.keystore,'--password-file',password,'--from',a.address,...(to?[to,data]:['--create',data])]
  const raw=cli(args);j={raw,hash:keccak256(raw),nonce,from:a.address,to,value:value.toString(),data};journal.transactions[name]=j;persist()
 }
 if(j.from.toLowerCase()!==a.address.toLowerCase()||j.to?.toLowerCase()!==to?.toLowerCase()||j.data!==data||BigInt(j.value)!==value||keccak256(j.raw)!==j.hash)throw new Error("Saved transaction parameters changed; inspect the existing journal")
 if(!j.receipt){for(let i=0;i<90;i++){let receipt=await rpc('eth_getTransactionReceipt',[j.hash]);if(receipt){const final=await rpc('eth_getBlockByNumber',['finalized',false]);if(BigInt(final.number)>=BigInt(receipt.blockNumber)){const canonical=await rpc('eth_getBlockByNumber',[receipt.blockNumber,false]);if(canonical.hash!==receipt.blockHash)throw new Error('Noncanonical receipt');if(receipt.status!=='0x1')throw new Error('Transaction reverted: '+name);j.receipt=receipt;break}}else if(i%4===0){try{await rpc('eth_sendRawTransaction',[j.raw])}catch{/* A timeout may follow a successful broadcast. */}}await sleep(750)}if(!j.receipt)throw new Error('Awaiting transaction: '+name)}
 report.transactions[name]={hash:j.hash,block:j.receipt.blockNumber,contract:j.receipt.contractAddress};persist();console.log('CONFIRMED',name,j.hash)
 return j.receipt
}
const abi=signatures=>parseAbi(signatures)
const tokenABI=abi(['function approve(address,uint256) returns(bool)','function allowance(address,address) view returns(uint256)','function balanceOf(address) view returns(uint256)'])
const payABI=JSON.parse(fs.readFileSync(path.join(contracts,'out/TokenSwoopPayGBatch.sol/TokenSwoopPayGBatch.json'))).abi
const reserveArtifact=JSON.parse(fs.readFileSync(path.join(contracts,'out/TokenSwoopReserveDebt.sol/TokenSwoopReserveDebt.json')))
const payArtifact=JSON.parse(fs.readFileSync(path.join(contracts,'out/TokenSwoopPayGBatch.sol/TokenSwoopPayGBatch.json')))
const reserveABI=reserveArtifact.abi
const data=(a,f,args=[])=>encodeFunctionData({abi:a,functionName:f,args})
async function read(to,a,f,args=[]){return decodeFunctionResult({abi:a,functionName:f,data:await rpc('eth_call',[{to,data:data(a,f,args)},'finalized'])})}
async function rejects(name,from,to,a,f,args=[]){let rejected=false;try{await rpc('eth_call',[{from,to,data:data(a,f,args)},'latest'])}catch(e){if(!e.message.startsWith('RPC rejected'))throw e;rejected=true}check(name,rejected)}
const tx=(name,role,to,a,f,args=[])=>send(phase+':'+name,role,to,data(a,f,args))
function oldValue(...keys){for(const k of keys){if(old[k])return old[k]}throw new Error('Deployment field unavailable: '+keys.join('/'))}
async function deploy(stop,name){
 const token=oldValue('kUSDC','token','paymentToken'),pool=oldValue('pool'),provider=account('provider').address
 const p=await send(name+':payment','deployer',null,encodeDeployData({abi:payABI,bytecode:payArtifact.bytecode.object,args:[token,stop,provider,provider]}))
 const r=await send(name+':reserve','deployer',null,encodeDeployData({abi:reserveABI,bytecode:reserveArtifact.bytecode.object,args:[token,pool,stop,provider,account('deployer').address]}))
 const payment=p.contractAddress,reserve=r.contractAddress
 await send(name+':buffer-approve','user',token,data(tokenABI,'approve',[reserve,1000000n]))
 await send(name+':buffer-fund','user',reserve,data(reserveABI,'fundBuffer',[1000000n]))
 const meta={chainId:10143,token,pool,aToken:await read(reserve,reserveABI,'aToken'),batch:payment,reserve,provider,operator:provider,owner:stop,manager:stop,stopAuthority:stop,treasury:account('deployer').address,mode:'debt',testnetMock:true,abiVersion:3,paymentBlock:p.blockNumber,reserveBlock:r.blockNumber,paymentCodeHash:keccak256(await rpc('eth_getCode',[payment,'latest'])),reserveCodeHash:keccak256(await rpc('eth_getCode',[reserve,'latest'])),tokenCodeHash:keccak256(await rpc('eth_getCode',[token,'latest']))}
 check(name+' fixed stop authority',(await read(payment,payABI,'stopAuthority')).toLowerCase()===stop.toLowerCase())
 check(name+' fixed provider',(await read(payment,payABI,'provider')).toLowerCase()===provider.toLowerCase())
 check(name+' reserve manager',(await read(reserve,reserveABI,'manager')).toLowerCase()===stop.toLowerCase())
 check(name+' reserve operator',(await read(reserve,reserveABI,'settlementOperator')).toLowerCase()===provider.toLowerCase())
 report[name]=meta;persist();return meta
}
async function main(){
 check('Monad Testnet chain ID',BigInt(await rpc('eth_chainId',[]))===10143n)
 if(phase==='test'){
  const name='tokenswoop-funds-stop-test',file=path.join(contracts,'.secrets/keystores',name)
  if(!fs.existsSync(file)){try{execFileSync(cast,['wallet','new',path.dirname(file),name,'--json'],{env:{...process.env,CAST_PASSWORD:fs.readFileSync(password,'utf8').trim()},stdio:['ignore','pipe','pipe']})}catch{throw new Error('Could not create test stop wallet')}}fs.chmodSync(file,0o600)
  const stop={address:cli(['wallet','address','--keystore',file,'--password-file',password]),keystore:file}
  await send('test:stop-gas','user',stop.address,'0x',80000000000000000n)
  const d=await deploy(stop.address,'temporary'),u=account('user').address,p=account('provider').address
  if(report.flow_completed){console.log('Existing completed flow retained');return}
  const bill=keccak256(toHex('funds-v3-payment-1')),repay=keccak256(toHex('funds-v3-payment-2')),debt=keccak256(toHex('funds-v3-debt-1'))
  await tx('api-allowance','user',d.token,tokenABI,'approve',[d.batch,20000000n])
  await tx('reserve-allowance','user',d.token,tokenABI,'approve',[d.reserve,50000000n])
  check('Reserve approval preserves API allowance',await read(d.token,tokenABI,'allowance',[u,d.batch])===20000000n)
  await tx('deposit','user',d.reserve,reserveABI,'deposit',[50000000n])
  check('Deposit principal',await read(d.reserve,reserveABI,'principalOf',[u])===50000000n)
  await tx('pay','provider',d.batch,payABI,'chargeBatch',[u,bill,12345n])
  await rejects('Duplicate batch cannot charge twice',p,d.batch,payABI,'chargeBatch',[u,bill,12345n])
  await rejects('Single charge shares duplicate protection',p,d.batch,payABI,'charge',[u,bill,12345n])
  await rejects('Provider cannot pause',p,d.batch,payABI,'pause')
  await tx('pause',stop,d.batch,payABI,'pause')
  await rejects('Stop blocks new charges',p,d.batch,payABI,'chargeBatch',[u,repay,10000000n])
  check('Paid bill remains observable while stopped',await read(d.batch,payABI,'isCharged',[u,bill]))
  await rejects('Stop authority cannot rotate',stop.address,d.batch,payABI,'transferOwnership',[u])
  await rejects('Stop authority cannot be renounced',stop.address,d.batch,payABI,'renounceOwnership')
  await tx('resume',stop,d.batch,payABI,'unpause')
  await tx('revoke-api','user',d.token,tokenABI,'approve',[d.batch,0n])
  await rejects('Revoked allowance prevents collection',p,d.batch,payABI,'chargeBatch',[u,repay,10000000n])
  await tx('report-debt','provider',d.reserve,reserveABI,'reportDebts',[u,[debt],[10000000n],0n])
  check('Only unpaid amount frozen',await read(d.reserve,reserveABI,'withdrawableOf',[u])===40000000n)
  await rejects('Cannot withdraw frozen principal',u,d.reserve,reserveABI,'withdraw',[40000001n])
  await rejects('Manager cannot report debt',stop.address,d.reserve,reserveABI,'reportDebts',[u,[debt],[1n],0n])
  await tx('partial-withdraw','user',d.reserve,reserveABI,'withdraw',[40000000n])
  check('Direct withdrawal needs no platform approval',await read(d.reserve,reserveABI,'principalOf',[u])===10000000n)
  await tx('restore-api','user',d.token,tokenABI,'approve',[d.batch,20000000n])
  await tx('repay','provider',d.batch,payABI,'chargeBatch',[u,repay,10000000n])
  await tx('release-paid-debt','provider',d.reserve,reserveABI,'reportDebts',[u,[debt],[0n],0n])
  check('Payment releases debt without seizing reserve',await read(d.reserve,reserveABI,'withdrawableOf',[u])===10000000n)
  const debt2=keccak256(toHex('funds-v3-debt-2'))
  await tx('second-debt','provider',d.reserve,reserveABI,'reportDebts',[u,[debt2],[5000000n],0n])
  await rejects('Provider cannot manager-clear debt',p,d.reserve,reserveABI,'clearDebt',[u])
  await tx('manager-clear',stop,d.reserve,reserveABI,'clearDebt',[u])
  check('Manager releases freeze',await read(d.reserve,reserveABI,'debtOf',[u])===0n)
  await rejects('Old epoch cannot refreeze cleared debt',p,d.reserve,reserveABI,'reportDebts',[u,[debt2],[5000000n],0n])
  await tx('pause-deposits',stop,d.reserve,reserveABI,'setDepositsPaused',[true])
  await rejects('Paused deposits rejected',u,d.reserve,reserveABI,'deposit',[1n])
  await tx('withdraw-while-paused','user',d.reserve,reserveABI,'withdraw',[10000000n])
  check('Deposit pause does not block withdrawals',await read(d.reserve,reserveABI,'principalOf',[u])===0n)
  await tx('resume-deposits',stop,d.reserve,reserveABI,'setDepositsPaused',[false])
  check('Deposits can resume',!(await read(d.reserve,reserveABI,'paused')))
  report.flow_completed=true;report.completed_at=new Date().toISOString();persist()
 }else if(phase==='cleanup-temporary'){
  const d=report.temporary,stop={address:d.stopAuthority,keystore:path.join(contracts,'.secrets/keystores/tokenswoop-funds-stop-test')}
  check('Temporary reserve is empty',await read(d.reserve,reserveABI,'totalPrincipal')===0n)
  await tx('revoke-allowance','user',d.token,tokenABI,'approve',[d.batch,0n])
  await tx('pause-payments',stop,d.batch,payABI,'pause')
  await tx('pause-deposits',stop,d.reserve,reserveABI,'setDepositsPaused',[true])
  report.temporary_closed=true;persist()
 }else if(phase==='retire-legacy'){
  if(!report.release)throw new Error('Deploy the replacement before retiring old deposits')
  const env=testnetEnvironment(root)
  let pending;try{pending=execFileSync('/opt/homebrew/opt/postgresql@18/bin/psql',[env.DATABASE_URL,'-tAc',"SELECT (SELECT count(*) FROM bills WHERE state<>'paid')+(SELECT count(*) FROM payment_attempts WHERE state='pending')+(SELECT count(*) FROM reserve_approvals WHERE state='pending')+(SELECT count(*) FROM requests WHERE state IN('accepted','in_flight'))"],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()}catch{throw new Error('Could not verify old settlement lane')}
  if(pending!=='0')throw new Error('Finish old pending requests and settlements before retirement')
  const oldABI=JSON.parse(fs.readFileSync(path.join(contracts,'out/TokenSwoopReserve.sol/TokenSwoopReserve.json'))).abi
  await tx('old-payment-pause','deployer',old.paygBatch,payABI,'pause')
  await tx('old-deposits-pause','deployer',old.reserve,oldABI,'pause')
  check('Old payment authority disabled',await read(old.paygBatch,payABI,'paused'))
  check('Old reserve deposits disabled',await read(old.reserve,oldABI,'paused'))
  check('Existing old reserve principal preserved',await read(old.reserve,oldABI,'totalPrincipal')===50000000n)
 }else if(phase.startsWith('backend-')){
  if(!report.flow_completed)throw new Error('Complete funds tests first')
  const d=report.temporary,u=account('user').address
  if(phase==='backend-setup'){
   await tx('allowance','user',d.token,tokenABI,'approve',[d.batch,20000000n])
   await tx('reserve-allowance','user',d.token,tokenABI,'approve',[d.reserve,50000000n])
   await tx('deposit','user',d.reserve,reserveABI,'deposit',[50000000n])
  }else if(phase==='backend-revoke')await tx('revoke','user',d.token,tokenABI,'approve',[d.batch,0n])
  else if(phase==='backend-restore')await tx('restore','user',d.token,tokenABI,'approve',[d.batch,20000000n])
  else if(phase==='backend-exit'){
   check('Worker automatically released reported debt',await read(d.reserve,reserveABI,'debtOf',[u])===0n)
   await tx('withdraw','user',d.reserve,reserveABI,'withdraw',[50000000n])
   check('Backend lifecycle returns all reserve principal',await read(d.reserve,reserveABI,'principalOf',[u])===0n)
  }else throw new Error('Unknown backend phase')
 }else if(phase==='deploy'){
  if(!report.flow_completed)throw new Error('Complete temporary authority tests before deployment')
  const d=await deploy('0x2B4f8239751025Adc001a2DF9F571f375e630c06','release')
  fs.writeFileSync(path.join(contracts,'deployments/monad-testnet-reserve-debt.json'),JSON.stringify(d,null,2)+'\n')
 }else throw new Error('Use test or deploy')
}
main().catch(e=>{console.error(e.message);process.exitCode=1})
