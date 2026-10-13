import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { parseEnv } from 'node:util'
import { networkValue, writeNetworkValues, testnetEnvironment } from './lib/network-env.mjs'
import { mainnetReader } from './lib/mainnet.mjs'

test('a selector cannot inherit testnet RPC, keystore or database on mainnet', () => {
  const env = { PAYMENT_RPC_URL: 'https://testnet.example', PAYMENT_KEYSTORE: '/test/key', DATABASE_URL: 'postgres://test/db', MAINNET_PAYMENT_RPC_URL: 'https://mainnet.example', TESTNET_PAYMENT_KEYSTORE: '' }
  assert.equal(networkValue(env, 'testnet', 'PAYMENT_RPC_URL'), env.PAYMENT_RPC_URL)
  assert.equal(networkValue(env, 'testnet', 'PAYMENT_KEYSTORE'), '')
  assert.equal(networkValue(env, 'mainnet', 'PAYMENT_RPC_URL'), env.MAINNET_PAYMENT_RPC_URL)
  assert.equal(networkValue(env, 'mainnet', 'PAYMENT_KEYSTORE'), '')
  assert.equal(networkValue(env, 'mainnet', 'DATABASE_URL'), '')
  assert.throws(() => networkValue(env, 'typo', 'DATABASE_URL'))
})

test('configuring one profile preserves active selection and the other profile', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tokenswoop-network-'))
  try {
    const file = path.join(root, '.env.local')
    fs.writeFileSync(file, 'CHAIN_NETWORK=testnet\nTESTNET_PAYMENT_RPC_URL=https://testnet.example\nMAINNET_PAYMENT_CONTRACT=old\n')
    writeNetworkValues(root, 'mainnet', { PAYMENT_CONTRACT: 'new', PAYMENT_RPC_URL: 'https://mainnet.example/path?x=1&y=2' })
    const values = parseEnv(fs.readFileSync(file, 'utf8'))
    assert.equal(values.CHAIN_NETWORK, 'testnet')
    assert.equal(values.TESTNET_PAYMENT_RPC_URL, 'https://testnet.example')
    assert.equal(values.MAINNET_PAYMENT_CONTRACT, 'new')
    assert.equal(values.MAINNET_PAYMENT_RPC_URL, 'https://mainnet.example/path?x=1&y=2')
    assert.equal(fs.statSync(file).mode & 0o777, 0o600)
    fs.writeFileSync(file, 'CHAIN_NETWORK=mainnet\n')
    assert.throws(() => testnetEnvironment(root), /requires CHAIN_NETWORK=testnet/)
  } finally { fs.rmSync(root, { recursive: true, force: true }) }
})

test('mainnet readiness reader rejects every broadcasting method', async () => {
  const { rpc } = mainnetReader({ MAINNET_PAYMENT_RPC_URL: 'https://mainnet.example' })
  for (const method of ['eth_sendRawTransaction', 'eth_sendTransaction', 'personal_sendTransaction'])
    await assert.rejects(rpc(method, []), /Read-only RPC method required/)
})
