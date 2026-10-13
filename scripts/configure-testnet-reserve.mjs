import {writeNetworkValues, readEnvironment, networkValue} from './lib/network-env.mjs'
// Read-only chain verification; enables the existing Reserve deployment locally.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import {fileURLToPath} from 'node:url'
import {execFileSync} from 'node:child_process'
import {testnetRPC} from './lib/testnet-rpc.mjs'
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..'), contracts=path.join(root,'contracts')
const read=p=>JSON.parse(fs.readFileSync(path.join(contracts,p),'utf8'))
const d=read('deployments/monad-testnet-reserve-debt.json'),w=read('deployments/testnet-wallets.json')
const vault=d.custody==='vault'
assert(d.chainId===10143 && d.mode==='debt' && (vault?d.abiVersion===4:d.testnetMock&&d.abiVersion===3),'Expected a supported debt testnet deployment')
const operator=Object.values(w.accounts).find(a=>a.address.toLowerCase()===d.operator.toLowerCase())
assert(operator,'No configured test operator keystore')
const rpc=testnetRPC(root,d.rpc),cast=path.join(contracts,'.toolchain/bin/cast')
async function call(method,params){const r=await fetch(rpc,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)});assert(r.ok);const v=await r.json();assert(!v.error && v.result!==undefined,`RPC ${method} failed`);return v.result}
assert.equal(BigInt(await call('eth_chainId',[])),10143n)
const code=await call('eth_getCode',[d.reserve,'latest']),artifact=read(vault?'out/TokenSwoopReserveVault.sol/TokenSwoopReserveVault.json':'out/TokenSwoopReserveDebt.sol/TokenSwoopReserveDebt.json')
const normalize=hex=>{const b=Buffer.from(hex.replace(/^0x/,''),'hex');for(const refs of Object.values(artifact.deployedBytecode.immutableReferences??{}))for(const {start,length}of refs)b.fill(0,start,start+length);return b.toString('hex')}
assert.equal(normalize(code),normalize(artifact.deployedBytecode.object),'Reserve code differs from compiled artifact')
for(const [sig,address] of [['reserveToken()',d.token],['settlementOperator()',d.operator],['manager()',d.manager],...(vault?[]:[['aavePool()',d.pool]])]){
 const data=execFileSync(cast,['sig',sig],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()
 const actual=await call('eth_call',[{to:d.reserve,data},'latest'])
 assert.equal(actual.slice(-40).toLowerCase(),address.slice(2).toLowerCase(),`${sig} mismatch`)
}
if(vault){
 const selector=execFileSync(cast,['sig','custodyMode()'],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()
 assert.equal(await call('eth_call',[{to:d.reserve,data:selector},'latest']),execFileSync(cast,['keccak','vault'],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim())
}
const hash=execFileSync(cast,['keccak',code],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()
const values={RESERVE_CUSTODY:vault?'vault':'aave',RESERVE_POOL:vault?'':d.pool.toLowerCase(),RESERVE_ATOKEN:vault?'':d.aToken.toLowerCase(),RESERVE_TREASURY:vault?'':d.treasury.toLowerCase(),RESERVE_ENABLED:'true',RESERVE_MODE:'debt',RESERVE_MANAGER:d.manager.toLowerCase(),RESERVE_CONTRACT:d.reserve.toLowerCase(),RESERVE_CODE_HASH:hash,RESERVE_OPERATOR:d.operator.toLowerCase(),RESERVE_KEYSTORE:path.join(contracts,operator.keystore),RESERVE_PASSWORD_FILE:path.join(contracts,w.passwordFile)}
const settings = readEnvironment(root)
assert.equal(networkValue(settings, 'testnet', 'SETTLEMENT_ENABLED'), 'true', 'Configure testnet payments first')
assert.equal(networkValue(settings, 'testnet', 'PAYMENT_TOKEN').toLowerCase(), d.token.toLowerCase(), 'Runtime token differs from Reserve token')
writeNetworkValues(root, 'testnet', values)
console.log('Existing Monad Testnet Reserve verified and configured. No transaction broadcast.')
