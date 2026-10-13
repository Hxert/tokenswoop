import test from 'node:test'
import assert from 'node:assert/strict'
import { sendExternal, checkAuthorization } from '../src/walletTransport.ts'

const account = '0x' + '11'.repeat(20)
const operation = {
  action: 'payment_approve',
  amount_units: '10',
  to: '0x' + '22'.repeat(20),
  data: '0x095ea7b3' + '0'.repeat(128),
  title: 'Approve',
  description: 'Set payment allowance',
}
const hash = '0x' + 'aa'.repeat(32)
test('external transport never broadcasts on wrong chain or changed account', async () => {
  let broadcasts = 0
  let chain = '0x1',
    accounts = [account]
  const provider = {
    request: async ({ method }) => {
      if (method === 'eth_requestAccounts') return [account]
      if (method === 'eth_accounts') return accounts
      if (method === 'eth_chainId') return chain
      if (method === 'eth_sendTransaction') {
        broadcasts++
        return hash
      }
    },
  }
  await assert.rejects(
    sendExternal(provider, account, operation),
    /Monad Testnet/,
  )
  chain = '0x279f'
  accounts = [operation.to]
  await assert.rejects(
    sendExternal(provider, account, operation),
    /account changed/,
  )
  assert.equal(broadcasts, 0)
})
test('external transport switches to Monad and binds the signed-in account', async () => {
  let chain = '0x1',
    transaction
  const provider = {
    request: async ({ method, params }) => {
      if (method === 'eth_requestAccounts' || method === 'eth_accounts')
        return [account]
      if (method === 'eth_chainId') return chain
      if (method === 'wallet_switchEthereumChain') {
        assert.deepEqual(params, [{ chainId: '0x279f' }])
        chain = '0x279f'
      }
      if (method === 'eth_sendTransaction') {
        transaction = params[0]
        return hash
      }
    },
  }
  assert.equal(await sendExternal(provider, account, operation), hash)
  assert.deepEqual(transaction, {
    from: account,
    to: operation.to,
    data: operation.data,
    value: '0x0',
    chainId: '0x279f',
  })
})
const authorization = () => ({
  version: 1,
  method: 'POST',
  url: 'https://api.privy.io/v1/wallets/my-wallet/rpc',
  body: {
    method: 'eth_sendTransaction',
    chain_type: 'ethereum',
    caip2: 'eip155:10143',
    reference_id: 'operation',
    sponsor: true,
    params: {
      transaction: { to: operation.to, data: operation.data, value: '0x0' },
    },
  },
  headers: {
    'privy-app-id': 'app',
    'privy-idempotency-key': 'operation',
    'privy-request-expiry': String(Date.now() + 60000),
  },
})
test('only the reviewed operation and fixed sponsorship envelope can be signed', () => {
  checkAuthorization(authorization(), 'app', 'operation', operation)
  for (const mutate of [
    (a) => (a.url = 'https://evil.example/wallets/my-wallet/rpc'),
    (a) => (a.body.caip2 = 'eip155:1'),
    (a) => (a.body.params.transaction.to = account),
    (a) => (a.body.params.transaction.data = '0x'),
    (a) => (a.body.params.transaction.value = '0x1'),
    (a) => (a.body.params.transaction.gas = '0xffffffff'),
    (a) => (a.body.sponsor = false),
    (a) => (a.body.reference_id = 'another-operation'),
    (a) => (a.body.sponsor_options = { payer: 'user' }),
    (a) => (a.headers['privy-app-id'] = 'another-app'),
    (a) => (a.headers['privy-idempotency-key'] = 'another-operation'),
    (a) => (a.headers['privy-request-expiry'] = String(Date.now() - 1000)),
    (a) => (a.headers['privy-request-expiry'] = 'NaN'),
  ]) {
    const a = authorization()
    mutate(a)
    assert.throws(() => checkAuthorization(a, 'app', 'operation', operation))
  }
})
