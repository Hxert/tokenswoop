import fs from 'node:fs'
import path from 'node:path'
import { parseEnv } from 'node:util'

export const chainKeys = [
  'DATABASE_URL', 'REDIS_URL', 'SETTLEMENT_ENABLED',
  'PAYMENT_RPC_URL', 'PAYMENT_RPC_RPS', 'PAYMENT_CONTRACT', 'PAYMENT_TOKEN',
  'PAYMENT_PROVIDER', 'PAYMENT_RECIPIENT', 'PAYMENT_STOP_AUTHORITY', 'PAYMENT_CONTRACT_CODE_HASH',
  'PAYMENT_TOKEN_CODE_HASH', 'PAYMENT_CAST_PATH', 'PAYMENT_KEYSTORE', 'PAYMENT_PASSWORD_FILE',
  'RESERVE_ENABLED', 'RESERVE_CUSTODY', 'RESERVE_MODE', 'RESERVE_MANAGER', 'RESERVE_CONTRACT',
  'RESERVE_CODE_HASH', 'RESERVE_OPERATOR', 'RESERVE_POOL', 'RESERVE_ATOKEN',
  'RESERVE_TREASURY', 'RESERVE_KEYSTORE', 'RESERVE_PASSWORD_FILE',
  'MERA_GAS_SPONSORSHIP_ENABLED', 'MERA_SPONSOR_MODE', 'MERA_IMPLEMENTATION', 'MERA_CODE_HASH',
  'MERA_SPONSOR_ADDRESS', 'MERA_KEYSTORE', 'MERA_PASSWORD_FILE',
  'MERA_DAILY_TRANSACTIONS', 'MERA_MAX_GAS_WEI', 'MERA_DAILY_GAS_WEI',
  'TURNSTILE_SITE_KEY', 'TURNSTILE_SECRET_KEY', 'FAUCET_ENABLED',
  'FAUCET_COOLDOWN_HOURS', 'FAUCET_IP_DAILY_LIMIT', 'FAUCET_DAILY_LIMIT',
]
export function readEnvironment(root) {
  const file = path.join(root, '.env.local')
  return { ...(fs.existsSync(file) ? parseEnv(fs.readFileSync(file, 'utf8')) : {}), ...process.env }
}
export function networkValue(env, network, key, fallback = '') {
  if (!['testnet', 'mainnet'].includes(network)) throw new Error('CHAIN_NETWORK must be testnet or mainnet')
  const scoped = `${network.toUpperCase()}_${key}`
  if (Object.hasOwn(env, scoped)) return env[scoped].trim()
  return network === 'testnet' ? env[key] || fallback : fallback
}
// For maintenance tools which still consume legacy variable names. Never let a
// testnet tool inherit the selected mainnet database, signer or RPC.
export function testnetEnvironment(root) {
  const env = readEnvironment(root)
  if (env.CHAIN_NETWORK === 'mainnet' || env.CHAIN_ID && env.CHAIN_ID !== '10143')
    throw new Error('This transaction test requires CHAIN_NETWORK=testnet')
  for (const key of chainKeys) env[key] = networkValue(env, 'testnet', key)
  env.CHAIN_NETWORK = 'testnet'
  return env
}
export function writeNetworkValues(root, network, values) {
  if (!['testnet', 'mainnet'].includes(network)) throw new Error('Unknown network')
  const file = path.join(root, '.env.local')
  let text = fs.readFileSync(file, 'utf8')
  for (const [key, value] of Object.entries(values)) {
    if (!chainKeys.includes(key) || /[\r\n\0]/.test(value)) throw new Error('Invalid network setting')
    // These values are sourced by dev.sh as well as parsed by Node. No shell expansion.
    if (!/^[A-Za-z0-9_:/.,?=&%+@~-]*$/.test(value)) throw new Error('Unsupported characters in network setting')
    const name = `${network.toUpperCase()}_${key}`
    const re = new RegExp(`^${name}=.*$`, 'm')
    const line = `${name}='${value}'`
    text = re.test(text) ? text.replace(re, () => line) : text.trimEnd() + '\n' + line + '\n'
  }
  fs.writeFileSync(file + '.tmp', text, { mode: 0o600 })
  fs.renameSync(file + '.tmp', file)
}
