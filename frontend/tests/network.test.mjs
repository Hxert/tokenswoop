import test from 'node:test'
import assert from 'node:assert/strict'
import { configureNetwork, network, networkStorageKey } from '../src/network.ts'
import { prepareProvider } from '../src/walletTransport.ts'

test('runtime network selection drives wallet switching and rejects unknown chains', async () => {
  const account='0x1111111111111111111111111111111111111111'
  let chain='0x279f'; const calls=[]
  const provider={request:async ({method,params})=>{calls.push({method,params});if(method==='eth_requestAccounts'||method==='eth_accounts')return [account];if(method==='eth_chainId')return chain;if(method==='wallet_switchEthereumChain')chain=params[0].chainId}}
  configureNetwork(143)
  try {
    await prepareProvider(provider,account)
    assert.equal(chain,'0x8f');assert.equal(network().testnet,false);assert.equal(network().symbol,'USDC')
    assert.throws(()=>configureNetwork(1),/Unsupported/)
    assert.ok(calls.some(c=>c.method==='wallet_switchEthereumChain'&&c.params[0].chainId==='0x8f'))
  } finally { configureNetwork(10143) }
  assert.equal(network().testnet,true)
})

test('the same wallet and Privy app have separate recoverable operation journals per chain', () => {
  const prefix = 'tokenswoop:wallet-operation'
  const wallet = '0x1111111111111111111111111111111111111111'
  configureNetwork(10143)
  const testnetKey = networkStorageKey(prefix, 'app', wallet)
  assert.equal(testnetKey, `${prefix}:app:${wallet}`)
  try {
    configureNetwork(143)
    assert.notEqual(networkStorageKey(prefix, 'app', wallet), testnetKey)
    assert.equal(network().chain.id, 143)
    assert.equal(network().caip2, 'eip155:143')
    assert.equal(network().symbol, 'USDC')
    assert.equal(network().publicRPC, 'https://rpc.monad.xyz')
  } finally { configureNetwork(10143) }
  assert.equal(networkStorageKey(prefix, 'app', wallet), testnetKey)
})
