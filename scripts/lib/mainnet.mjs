import { createRequire } from 'node:module'
import { networkValue } from './network-env.mjs'

const require = createRequire(new URL('../../frontend/package.json', import.meta.url))
export const { encodeFunctionData, decodeFunctionResult, parseAbi, keccak256 } = require('viem')
export const mainnet = {
  chainId: 143,
  token: '0x754704Bc059F8C67012fEd69BC8A327a5aafb603',
}
// Maintenance checks are paced and deliberately cannot send transactions.
export function mainnetReader(env) {
  const endpoint = networkValue(env, 'mainnet', 'PAYMENT_RPC_URL')
  let url
  try { url = new URL(endpoint) } catch { throw new Error('Configure MAINNET_PAYMENT_RPC_URL') }
  if (url.protocol !== 'https:' || !url.hostname || url.username || url.password)
    throw new Error('MAINNET_PAYMENT_RPC_URL must be HTTPS')
  const rps = Number(networkValue(env, 'mainnet', 'PAYMENT_RPC_RPS', '5'))
  if (!Number.isInteger(rps) || rps < 1 || rps > 1000) throw new Error('Invalid mainnet RPC rate')
  let last = 0
  async function rpc(method, params) {
    if (!['eth_chainId', 'eth_getCode', 'eth_call', 'eth_getBlockByNumber', 'eth_getLogs', 'eth_getTransactionReceipt', 'eth_getBalance', 'eth_gasPrice', 'eth_getTransactionCount', 'eth_estimateGas'].includes(method))
      throw new Error('Read-only RPC method required')
    await new Promise(resolve => setTimeout(resolve, Math.max(0, last + Math.ceil(1000 / rps) - Date.now())))
    last = Date.now()
    try {
      const res = await fetch(endpoint, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }), signal: AbortSignal.timeout(20000), redirect: 'error' })
      if (!res.ok) throw new Error()
      const value = await res.json()
      if (value.error || value.result === undefined) throw new Error()
      return value.result
    } catch { throw new Error(`Mainnet RPC ${method} failed (endpoint redacted)`) }
  }
  async function call(to, signature, args = [], block = 'latest') {
    const abi = parseAbi([signature])
    const functionName = abi[0].name
    const data = encodeFunctionData({ abi, functionName, args })
    return decodeFunctionResult({ abi, functionName, data: await rpc('eth_call', [{ to, data }, block]) })
  }
  return { rpc, call }
}
