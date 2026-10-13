// Read-only quote: state overrides apply only to eth_estimateGas, never to chain state.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { readEnvironment, networkValue } from './lib/network-env.mjs'
import { mainnet, mainnetReader } from './lib/mainnet.mjs'
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { encodeDeployData, getAddress, formatUnits } = require('viem')
const root = fileURLToPath(new URL('../', import.meta.url))
try {
  const env = readEnvironment(root), value = key => networkValue(env, 'mainnet', key)
  const roles = {
    deployer: getAddress(env.MAINNET_DEPLOYER_ADDRESS),
    provider: getAddress(value('PAYMENT_PROVIDER')),
    reserveOperator: getAddress(value('RESERVE_OPERATOR')),
    stopAuthority: getAddress(value('PAYMENT_STOP_AUTHORITY')),
    reserveManager: getAddress(value('RESERVE_MANAGER')),
    revenueRecipient: getAddress(value('PAYMENT_RECIPIENT')),
  }
  assert(roles.revenueRecipient !== roles.provider, 'Revenue must go to its independent recipient')
  assert.equal(value('RESERVE_CUSTODY'), 'vault')
  assert.equal(value('PAYMENT_TOKEN').toLowerCase(), mainnet.token.toLowerCase())
  const {rpc,call}=mainnetReader(env)
  assert.equal(BigInt(await rpc('eth_chainId',[])),143n)
  const block=await rpc('eth_getBlockByNumber',['finalized',false])
  assert(block?.hash && Math.abs(Date.now()/1000-Number(BigInt(block.timestamp)))<120)
  const gasPrice=BigInt(await rpc('eth_gasPrice',[])),balances={}
  for(const address of new Set(Object.values(roles))){
    const mon=BigInt(await rpc('eth_getBalance',[address,block.number]))
    const usdc=await call(mainnet.token,'function balanceOf(address) view returns (uint256)',[address],block.number)
    balances[address]={MON:formatUnits(mon,18),USDC:formatUnits(usdc,6)}
  }
  const contracts=[]
  for(const [name,args] of [
    ['TokenSwoopPayGBatch',[mainnet.token,roles.stopAuthority,roles.provider,roles.revenueRecipient]],
    ['TokenSwoopReserveVault',[mainnet.token,roles.reserveManager,roles.reserveOperator]],
  ]){
    const artifact=JSON.parse(fs.readFileSync(new URL(`../contracts/out/${name}.sol/${name}.json`,import.meta.url),'utf8'))
    const data=encodeDeployData({abi:artifact.abi,bytecode:artifact.bytecode.object,args})
    const gas=BigInt(await rpc('eth_estimateGas',[{from:roles.deployer,data,value:'0x0'},block.number,{[roles.deployer]:{balance:'0x8ac7230489e80000'}}]))
    const gasLimit=(gas*130n+99n)/100n
    contracts.push({name,estimatedGas:gas.toString(),gasLimitWith30PercentMargin:gasLimit.toString(),estimatedMON:formatUnits(gas*gasPrice,18),budgetMONAtCurrentPrice:formatUnits(gasLimit*gasPrice,18)})
  }
  const sum=(key)=>contracts.reduce((v,c)=>v+BigInt(c[key]),0n)
  const targetBalances=[
    {role:'deployer',address:roles.deployer,targetMON:'1',requiredUSDC:'0',purpose:'Deploy both contracts; includes headroom over the RPC quote'},
    {role:'provider / reserve operator',address:roles.provider,targetMON:'5',requiredUSDC:'0',purpose:'Initial operating gas; replenish as used'},
    {role:'stop authority / reserve manager',address:roles.stopAuthority,targetMON:'0.1',requiredUSDC:'0',optionalForDeployment:true,purpose:'Pause/resume payments or deposits and clear debt'},
    {role:'revenue recipient',address:roles.revenueRecipient,targetMON:'0',requiredUSDC:'0',purpose:'Receives API USDC directly; no receiving gas'},
  ]
  for(const row of targetBalances){
    const units=s=>{const [a,b='']=s.split('.');return BigInt(a)*10n**18n+BigInt(b.padEnd(18,'0'))}
    const deficit=units(row.targetMON)-units(balances[row.address].MON)
    row.topUpMON=formatUnits(deficit>0n?deficit:0n,18)
  }
  const report={checkedAt:new Date().toISOString(),readOnly:true,chainId:143,block:Number(BigInt(block.number)),blockHash:block.hash,nativeUSDC:mainnet.token,reserveCustody:'vault',requiresUSDCBuffer:false,roles,balances,gasPriceWei:gasPrice.toString(),gasPriceGwei:formatUnits(gasPrice,9),contracts,estimatedDeploymentMON:formatUnits(sum('estimatedGas')*gasPrice,18),deploymentBudgetMONAtCurrentPrice:formatUnits(sum('gasLimitWith30PercentMargin')*gasPrice,18),funding:targetBalances,mainnetContractsDeployed:fs.existsSync(new URL('../contracts/deployments/monad-mainnet-reserve-debt.json',import.meta.url)),activeNetwork:env.CHAIN_NETWORK,notes:['Quotes can change before inclusion; Monad charges the gas limit, not just gas used.','No USDC is required to deploy or operate the provider. User testing uses the user wallet\'s USDC.','No transactions signed or broadcast. No Reserve principal is transferred to the revenue recipient.'],sources:['https://docs.monad.xyz/developer-essentials/gas-pricing','https://developers.circle.com/stablecoins/usdc-contract-addresses']}
  fs.writeFileSync(new URL('../contracts/reports/mainnet-funding-estimate.json',import.meta.url),JSON.stringify(report,null,2)+'\n')
  console.log(JSON.stringify(report,null,2))
} catch(error){console.error(error.message);process.exitCode=1}
