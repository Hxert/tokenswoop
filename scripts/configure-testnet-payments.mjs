import {writeNetworkValues, readEnvironment, networkValue} from './lib/network-env.mjs'
// Configure the local development runtime from the existing, verified deployment.
// This command reads the chain; it does not sign or broadcast transactions.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFileSync} from 'node:child_process';
import {testnetRPC} from './lib/testnet-rpc.mjs';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..'),contracts=path.join(root,'contracts');
const read=p=>JSON.parse(fs.readFileSync(path.join(contracts,p),'utf8'));
const debtFile=path.join(contracts,'deployments/monad-testnet-reserve-debt.json');
const deployment=fs.existsSync(debtFile)?JSON.parse(fs.readFileSync(debtFile)):read('deployments/monad-testnet.json');
const d={...deployment,kUSDC:deployment.kUSDC||deployment.token,paygBatch:deployment.paygBatch||deployment.batch},w=read('deployments/testnet-wallets.json');
assert(d.chainId===10143&&w.chainId===10143&&d.provider.toLowerCase()===w.accounts.provider.address.toLowerCase());
const rpc=testnetRPC(root,d.rpc);
assert(new URL(rpc).protocol==='https:');
async function call(method,params){const res=await fetch(rpc,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params}),signal:AbortSignal.timeout(15000)});assert(res.ok);const v=await res.json();assert(!v.error&&v.result!==undefined,'RPC call failed');return v.result}
assert(BigInt(await call('eth_chainId',[]))===10143n);
const hashes={};
for(const [name,address] of [['KUSDC',d.kUSDC],['TokenSwoopPayGBatch',d.paygBatch]]){
 const code=await call('eth_getCode',[address,'latest']),a=read(`out/${name}.sol/${name}.json`);
 const normalize=hex=>{const b=Buffer.from(hex.replace(/^0x/,''),'hex');for(const entries of Object.values(a.deployedBytecode.immutableReferences??{}))for(const {start,length}of entries)b.fill(0,start,start+length);return b.toString('hex')};
 hashes[name]=execFileSync(path.join(contracts,'.toolchain/bin/cast'),['keccak',code],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim();
 if(name==='TokenSwoopPayGBatch' && !d.recipient && d.abiVersion===3 && d.paymentCodeHash) {
  // Existing testnet provider-as-recipient bytecode was pinned before the split.
  assert.equal(hashes[name],d.paymentCodeHash,'Existing testnet runtime changed');
 } else assert.equal(normalize(code),normalize(a.deployedBytecode.object),'Deployed bytecode differs from the compiled contract');
}
const values={SETTLEMENT_ENABLED:'true',PAYMENT_RPC_URL:rpc,PAYMENT_CONTRACT:d.paygBatch.toLowerCase(),PAYMENT_TOKEN:d.kUSDC.toLowerCase(),PAYMENT_PROVIDER:d.provider.toLowerCase(),PAYMENT_CONTRACT_CODE_HASH:hashes.TokenSwoopPayGBatch,PAYMENT_TOKEN_CODE_HASH:hashes.KUSDC,PAYMENT_CAST_PATH:path.join(contracts,'.toolchain/bin/cast'),PAYMENT_KEYSTORE:path.join(contracts,w.accounts.provider.keystore),PAYMENT_PASSWORD_FILE:path.join(contracts,w.passwordFile)};
values.PAYMENT_RECIPIENT=d.recipient?.toLowerCase()||'';
if(d.stopAuthority)values.PAYMENT_STOP_AUTHORITY=d.stopAuthority.toLowerCase();
for(const [signature,expected] of [['provider()',d.provider],['paymentToken()',d.kUSDC],...(d.recipient?[['recipient()',d.recipient]]:[]),...(d.stopAuthority?[['owner()',d.stopAuthority]]:[])]){
 const data=execFileSync(path.join(contracts,'.toolchain/bin/cast'),['sig',signature],{encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim();
 const actual=await call('eth_call',[{to:d.paygBatch,data},'latest']);
 assert.equal(actual.slice(-40).toLowerCase(),expected.slice(2).toLowerCase(),signature+' mismatch');
}
writeNetworkValues(root, 'testnet', values)
console.log('Local wallet settlement configured for Monad Testnet. Pinned runtime and fixed payment roles verified. No transaction broadcast.');
