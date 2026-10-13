import test from 'node:test'
import assert from 'node:assert/strict'
import {
  reserveUnits,
  formatReserveUnits,
  reserveCall,
  reserveWallet,
} from '../src/reserveWallet.ts'
test('reserve amounts stay exact at six decimals and reject invalid amounts', () => {
  assert.equal(reserveUnits('1000.000001'), 1000000001n)
  assert.equal(
    formatReserveUnits('123456789012345678'),
    '123,456,789,012.345678',
  )
  for (const bad of ['0', '-1', '1e2', '0.0000001', '1.', '01', 'Infinity'])
    assert.throws(() => reserveUnits(bad))
})
test('reserve allowance targets only Reserve, withdrawal encodes the requested amount', () => {
  const contract = '0x' + '11'.repeat(20)
  assert.equal(
    reserveCall('approve', contract, 50_000_000n),
    '0x095ea7b3' +
      contract.slice(2).padStart(64, '0') +
      '2faf080'.padStart(64, '0'),
  )
  assert.equal(
    reserveCall('deposit', contract, 50_000_000n),
    '0xb6b55f25' + '2faf080'.padStart(64, '0'),
  )
  assert.equal(
    reserveCall('withdraw', contract, 7_000_001n),
    '0x2e1a7d4d' + (7_000_001).toString(16).padStart(64, '0'),
  )
  assert.throws(() => reserveCall('cancel', contract, 1n))
  assert.throws(() => reserveCall('request', contract, 1n))
  assert.throws(() => reserveCall('deposit', contract, 0n))
  assert.throws(() => reserveCall('deposit', contract, 2n ** 256n))
})
test('wallet validation rejects a wrong network and changed account', async () => {
  const account = '0x' + '11'.repeat(20)
  globalThis.window = {
    ethereum: {
      request: async ({ method }) =>
        method === 'eth_chainId' ? '0x1' : [account],
    },
  }
  await assert.rejects(() => reserveWallet(account), /Monad Testnet/)
  window.ethereum.request = async ({ method }) =>
    method === 'eth_chainId'
      ? '0x279f'
      : method === 'eth_accounts'
        ? ['0x' + '22'.repeat(20)]
        : [account]
  await assert.rejects(() => reserveWallet(account), /account changed/)
  delete globalThis.window
})
