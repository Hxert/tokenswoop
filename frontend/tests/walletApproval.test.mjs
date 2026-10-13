import test from 'node:test'
import assert from 'node:assert/strict'
import { checkApprovalBalance } from '../src/walletApproval.ts'
import { configureNetwork } from '../src/network.ts'

globalThis.document = { cookie: 'ts_csrf=test' }
const account = '0x' + '11'.repeat(20),
  token = '0x' + '22'.repeat(20)
const operation = (action, amount) => ({
  action,
  amount_units: amount.toString(),
  to: token,
  data: '0x',
  title: 'Approve',
  description: '',
})
function balance(value) {
  let calls = 0
  globalThis.fetch = async (route, init) => {
    calls++
    assert.equal(route, '/api/rpc')
    const rpc = JSON.parse(init.body)
    assert.equal(rpc.method, 'eth_call')
    assert.equal(rpc.params[0].to, token)
    assert.equal(
      rpc.params[0].data,
      '0x70a08231' + account.slice(2).padStart(64, '0'),
    )
    assert.equal(rpc.params[1], 'latest')
    return Response.json({
      result: '0x' + value.toString(16).padStart(64, '0'),
    })
  }
  return () => calls
}
test('payment and Reserve approvals cannot exceed a fresh wallet balance, including one micro-USDC', async () => {
  configureNetwork(143)
  for (const action of ['payment_approve', 'reserve_approve']) {
    const calls = balance(10000000n)
    await assert.rejects(
      checkApprovalBalance(account, operation(action, 10000001n)),
      (e) =>
        e.beforeSubmission &&
        e.code === 'insufficient_wallet_balance' &&
        /10 USDC/.test(e.message) &&
        /10.000001 USDC/.test(e.message),
    )
    await checkApprovalBalance(account, operation(action, 10000000n))
    await checkApprovalBalance(account, operation(action, 9999999n))
    assert.equal(calls(), 3)
    balance(0n)
    await assert.rejects(
      checkApprovalBalance(account, operation(action, 1n)),
      /Insufficient wallet balance/,
    )
  }
})
test('revoking allowance works with zero balance and withdrawal does not require wallet tokens', async () => {
  const calls = balance(0n)
  for (const action of ['payment_approve', 'reserve_approve'])
    await checkApprovalBalance(account, operation(action, 0n))
  await checkApprovalBalance(account, operation('reserve_withdraw', 50000000n))
  assert.equal(calls(), 0)
})
test('RPC failure blocks approval before submission instead of assuming funds are available', async () => {
  globalThis.fetch = async () => {
    throw new TypeError('Disconnected')
  }
  await assert.rejects(
    checkApprovalBalance(account, operation('payment_approve', 1n)),
    (e) => e.beforeSubmission === true,
  )
})
