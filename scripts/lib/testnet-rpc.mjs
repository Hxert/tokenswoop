import { readEnvironment, networkValue } from './network-env.mjs'

// Reconfiguration must not overwrite the runtime's private RPC with the public
// URL saved in a historical deployment manifest. Never print this return value.
export function testnetRPC(root, fallback) {
  const configured = networkValue(readEnvironment(root), 'testnet', 'PAYMENT_RPC_URL')
  const endpoint = process.env.MONAD_TESTNET_RPC_URL || configured || fallback
  let url
  try { url = new URL(endpoint) } catch { throw new Error('Invalid testnet RPC configuration') }
  if (url.protocol !== 'https:' || url.username || url.password)
    throw new Error('Testnet RPC must use HTTPS without userinfo')
  return endpoint
}
