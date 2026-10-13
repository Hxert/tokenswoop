// Deployment and funding are journaled before broadcast. This script never prints key material.
import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { encodeDeployData, keccak256, toHex, formatEther } from '../frontend/node_modules/viem/_esm/index.js'
import { readEnvironment, networkValue, writeNetworkValues } from './lib/network-env.mjs'

const root=path.resolve(import.meta.dirname,'..'), contracts=path.join(root,'contracts')
const env=readEnvironment(root), network=process.argv[2]
if (!['testnet','mainnet'].includes(network)) throw new Error('Select testnet or mainnet explicitly')
const chainID=network==='mainnet'?143:10143, value=k=>networkValue(env,network,k)
const url=value('PAYMENT_RPC_URL'), cast=path.join(contracts,'.toolchain/bin/cast')
const wallets=JSON.parse(fs.readFileSync(path.join(contracts,'deployments/testnet-wallets.json')))
const deployer=wallets.accounts.deployer, password=path.resolve(contracts,wallets.passwordFile)
const signer=path.resolve(contracts,deployer.keystore)
const sponsorMode=value('MERA_SPONSOR_MODE')||'dedicated'
if(!['platform','dedicated'].includes(sponsorMode))throw new Error('Invalid MERA_SPONSOR_MODE')
const sponsorFile=sponsorMode==='platform'?value('PAYMENT_KEYSTORE'):path.join(contracts,'.secrets/keystores',`mera-gas-${network}`)
const sponsorPassword=sponsorMode==='platform'?value('PAYMENT_PASSWORD_FILE'):password
const journalFile=path.join(root,'.local',`mera-deploy-${network}.json`)
const manifestFile=path.join(contracts,'deployments',`mera-${network}.json`)
const journal=fs.existsSync(journalFile)?JSON.parse(fs.readFileSync(journalFile)):{chainID,transactions:{}}
fs.mkdirSync(path.dirname(journalFile),{recursive:true})
const persist=()=>{fs.writeFileSync(journalFile+'.tmp',JSON.stringify(journal,null,2),{mode:0o600});fs.renameSync(journalFile+'.tmp',journalFile)}
const sleep=ms=>new Promise(r=>setTimeout(r,ms))
async function rpc(method,params){
 try{const r=await fetch(url,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)}).then(r=>r.json());if(r.error)throw new Error();return r.result}catch{throw new Error(`RPC ${method} failed (endpoint withheld)`)}
}
function cli(args, extra={}) {try{return execFileSync(cast,args,{cwd:contracts,env:{...process.env,...extra},encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()}catch{throw new Error('Local signer failed (sensitive output withheld)')}}
async function send(name,to,data='0x',amount=0n) {
 let item=journal.transactions[name]
 if(!item){
  const nonce=await rpc('eth_getTransactionCount',[deployer.address,'pending'])
  if(nonce!==await rpc('eth_getTransactionCount',[deployer.address,'latest']))throw new Error('Deployer has another pending transaction')
  const price=BigInt(await rpc('eth_gasPrice',[]))*2n
  const gas=BigInt(await rpc('eth_estimateGas',[{from:deployer.address,...(to?{to}:{}),data,value:toHex(amount)}]))*125n/100n
  if(gas*price>300000000000000000n)throw new Error('Deployment exceeds 0.30 MON gas budget')
  const balance=BigInt(await rpc('eth_getBalance',[deployer.address,'latest']))
  if(balance<gas*price+amount)throw new Error('Insufficient deployer funding')
  const raw=cli(['mktx','--legacy','--chain',String(chainID),'--nonce',BigInt(nonce).toString(),'--gas-limit',gas.toString(),'--gas-price',price.toString(),'--value',amount.toString(),'--keystore',signer,'--password-file',password,'--from',deployer.address,...(to?[to,data]:['--create',data])])
  item={raw,hash:keccak256(raw),to,data,amount:amount.toString()};journal.transactions[name]=item;persist()
 }
 if(item.to!==to||item.data!==data||item.amount!==amount.toString()||keccak256(item.raw)!==item.hash)throw new Error('Deployment journal parameters changed')
 for(let i=0;i<100&&!item.receipt;i++){
  const r=await rpc('eth_getTransactionReceipt',[item.hash])
  if(r){
   const final=await rpc('eth_getBlockByNumber',['finalized',false])
   if(BigInt(final.number)>=BigInt(r.blockNumber)){
    const canonical=await rpc('eth_getBlockByNumber',[r.blockNumber,false])
    if(r.status!=='0x1'||canonical.hash!==r.blockHash)throw new Error('Transaction reverted or noncanonical')
    item.receipt=r;persist();break
   }
  }else if(i%4===0){try{await rpc('eth_sendRawTransaction',[item.raw])}catch{/* Recover the exact journaled bytes. */}}
  await sleep(800)
 }
 if(!item.receipt)throw new Error('Transaction remains pending; rerun to recover it')
 console.log('Confirmed',network,name,item.hash)
 return item.receipt
}
async function main(){
 if(BigInt(await rpc('eth_chainId',[]))!==BigInt(chainID))throw new Error('RPC chain mismatch')
 if(network==='mainnet'){
  const report=JSON.parse(fs.readFileSync(path.join(root,'.local/qa/mera-testnet/report.json')))
  if(!report.completed)throw new Error('Complete the testnet sponsorship flow first')
 }
 if(sponsorMode==='dedicated'&&!fs.existsSync(sponsorFile)){
  cli(['wallet','new',path.dirname(sponsorFile),path.basename(sponsorFile)],{CAST_PASSWORD:fs.readFileSync(password,'utf8').trim()})
  fs.chmodSync(sponsorFile,0o600)
 }
 const sponsor=cli(['wallet','address','--keystore',sponsorFile,'--password-file',sponsorPassword])
 if(sponsorMode==='platform'&&sponsor.toLowerCase()!==value('PAYMENT_PROVIDER').toLowerCase())throw new Error('Platform sponsor keystore does not match provider')
 const artifact=JSON.parse(fs.readFileSync(path.join(contracts,'out/TokenSwoopMeraAccount.sol/TokenSwoopMeraAccount.json')))
 const token=value('PAYMENT_TOKEN'),payment=value('PAYMENT_CONTRACT'),reserve=value('RESERVE_CONTRACT')
 for(const target of [token,payment,reserve])if(await rpc('eth_getCode',[target,'latest'])==='0x')throw new Error('Configured target is not a contract')
 const deployed=await send('implementation',null,encodeDeployData({abi:artifact.abi,bytecode:artifact.bytecode.object,args:[token,payment,reserve]}))
 const implementation=deployed.contractAddress,code=await rpc('eth_getCode',[implementation,'latest']),codeHash=keccak256(code)
 if(code==='0x')throw new Error('No deployed implementation')
 // Reusing the already funded platform signer needs no new wallet or transfer.
 if(sponsorMode==='dedicated'){
  await send('sponsor-funding',sponsor,'0x',100000000000000000n)
  if(network==='testnet')await send('reserve-test-gas',sponsor,'0x',150000000000000000n)
  else await send('mainnet-starter-gas',sponsor,'0x',200000000000000000n)
 }
 const previous=fs.existsSync(manifestFile)?JSON.parse(fs.readFileSync(manifestFile)):null
 const previousSponsors=previous?.previousSponsors||[]
 if(previous?.sponsor&&previous.sponsor.toLowerCase()!==sponsor.toLowerCase())previousSponsors.push({address:previous.sponsor,...(previous.fundingTransaction?{fundingTransaction:previous.fundingTransaction}:{})})
 const manifest={chainID,implementation,codeHash,sponsorMode,sponsor,token,payment,reserve,deploymentTransaction:journal.transactions.implementation.hash,...(sponsorMode==='dedicated'?{fundingTransaction:journal.transactions['sponsor-funding'].hash}:{}),...(previousSponsors.length?{previousSponsors}:{})}
 fs.writeFileSync(manifestFile,JSON.stringify(manifest,null,2)+'\n')
 writeNetworkValues(root,network,{MERA_GAS_SPONSORSHIP_ENABLED:'true',MERA_SPONSOR_MODE:sponsorMode,MERA_IMPLEMENTATION:implementation,MERA_CODE_HASH:codeHash,MERA_SPONSOR_ADDRESS:sponsorMode==='dedicated'?sponsor:'',MERA_KEYSTORE:sponsorMode==='dedicated'?sponsorFile:'',MERA_PASSWORD_FILE:sponsorMode==='dedicated'?sponsorPassword:'',MERA_DAILY_TRANSACTIONS:'30',MERA_MAX_GAS_WEI:network==='testnet'?'150000000000000000':'50000000000000000',MERA_DAILY_GAS_WEI:'1000000000000000000'})
 console.log(JSON.stringify({...manifest,sponsorMON:formatEther(BigInt(await rpc('eth_getBalance',[sponsor,'latest'])))},null,2))
}
main().catch(e=>{console.error(e.message);process.exitCode=1})
