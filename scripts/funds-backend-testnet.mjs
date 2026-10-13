import {testnetEnvironment, chainKeys} from './lib/network-env.mjs'
import fs from 'node:fs'
import path from 'node:path'
import {execFileSync,spawnSync} from 'node:child_process'
const root=path.resolve(import.meta.dirname,'..'),env=testnetEnvironment(root)
const d=JSON.parse(fs.readFileSync(path.join(root,'.local/qa/funds-testnet/report.json'))).temporary
const wallets=JSON.parse(fs.readFileSync(path.join(root,'contracts/deployments/testnet-wallets.json')))
const url=new URL(env.DATABASE_URL);if(!['127.0.0.1','localhost'].includes(url.hostname))throw new Error('Local database required')
const pg=['-h',url.hostname,'-p',url.port,'-U',decodeURIComponent(url.username)],pgEnv={...process.env,PGPASSWORD:decodeURIComponent(url.password)}
const name='tokenswoop_funds_testnet_v3';const psql='/opt/homebrew/opt/postgresql@18/bin/psql'
const exists=execFileSync(psql,[...pg,'-d','postgres','-tAc',`SELECT 1 FROM pg_database WHERE datname='${name}'`],{env:pgEnv,encoding:'utf8',stdio:['ignore','pipe','pipe']}).trim()
if(!exists)execFileSync('/opt/homebrew/opt/postgresql@18/bin/createdb',[...pg,name],{env:pgEnv,stdio:['ignore','pipe','pipe']})
url.pathname='/'+name
Object.assign(env,{DATABASE_URL:url.toString(),FUNDS_TESTNET_DATABASE:url.toString(),FUNDS_TESTNET_WALLET:wallets.accounts.user.address.toLowerCase(),CHAIN_NETWORK:'testnet',PAYMENT_CONTRACT:d.batch,PAYMENT_CONTRACT_CODE_HASH:d.paymentCodeHash,PAYMENT_STOP_AUTHORITY:d.stopAuthority,RESERVE_CONTRACT:d.reserve,RESERVE_CODE_HASH:d.reserveCodeHash,RESERVE_MANAGER:d.manager,RESERVE_MODE:'debt',GOCACHE:path.join(root,'backend/.cache/go-build'),GOMODCACHE:path.join(root,'backend/.cache/gomod'),GOPATH:path.join(root,'backend/.cache/gopath')})
// Fixture contract overrides must win over the running testnet profile.
for (const key of chainKeys) { delete env['TESTNET_'+key]; delete env['MAINNET_'+key] }
delete env.RESERVE_POOL; delete env.RESERVE_ATOKEN; delete env.RESERVE_TREASURY
const phase=process.argv[2];if(!['prepare','fail','submit','recover','reconcile-proof'].includes(phase))throw new Error('Choose prepare/fail/submit/recover/reconcile-proof')
env.FUNDS_TESTNET_PHASE=phase
const log=fs.openSync(path.join(root,`.local/qa/funds-testnet/backend-${phase}.log`),'w')
const r=spawnSync('go',['test','-v','-count=1','-run','^TestFundsLifecycleTestnet$','./internal/httpapi'],{cwd:path.join(root,'backend'),env,stdio:['ignore',log,log]});fs.closeSync(log);console.log('Funds backend phase',phase,'exit',r.status);process.exitCode=r.status??1
