import test from 'node:test'
import assert from 'node:assert/strict'
import { mnemonicToAccount, privateKeyToAccount } from 'viem/accounts'
import { keccak256, recoverMessageAddress } from 'viem'
import { configureNetwork, networkStorageKey } from '../src/network.ts'
import {
  withPrfAccount,
  withMeraWallet,
  meraDerivationPath,
  meraCredentialsKey,
  pendingMeraTransaction,
  validateMeraTransaction,
  resumeMeraTransaction,
} from '../src/meraWallet.ts'

const storage = new Map()
Object.defineProperty(globalThis, 'document', {
  value: { cookie: 'ts_csrf=test-session' },
  configurable: true,
})
Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (key) => storage.get(key) ?? null,
    setItem: (key, value) => storage.set(key, value),
    removeItem: (key) => storage.delete(key),
  },
  configurable: true,
})
Object.defineProperty(globalThis, 'window', {
  value: { isSecureContext: true },
  configurable: true,
})
Object.defineProperty(globalThis, 'location', {
  value: { hostname: 'localhost' },
  configurable: true,
})
Object.defineProperty(globalThis, 'PublicKeyCredential', {
  value: class {},
  configurable: true,
})
Object.defineProperty(globalThis.navigator, 'credentials', {
  value: {},
  configurable: true,
})
const locks = new Set()
Object.defineProperty(globalThis.navigator, 'locks', {
  value: {
    request: async (key, _options, run) => {
      if (locks.has(key)) return run(null)
      locks.add(key)
      try {
        return await run({})
      } finally {
        locks.delete(key)
      }
    },
  },
  configurable: true,
})
const fixturePhrase = 'abandon '.repeat(23) + 'art'
const fixtureAccount = mnemonicToAccount(fixturePhrase, {
  path: meraDerivationPath,
})

test('Mera PRF creates a recoverable EVM wallet with valid EIP-191 signatures; keys expire after use', async () => {
  const entropy = new Uint8Array(32)
  let released
  const address = await withPrfAccount(entropy, async (account, phrase) => {
    released = account
    assert.equal(phrase, fixturePhrase)
    assert.equal(account.address, fixtureAccount.address)
    const signature = await account.signMessage({
      message: 'Server-issued TokenSwoop challenge',
    })
    assert.equal(
      await recoverMessageAddress({
        message: 'Server-issued TokenSwoop challenge',
        signature,
      }),
      account.address,
    )
    return account.address
  })
  assert.ok(entropy.every((v) => v === 0))
  await assert.rejects(
    released.signMessage({ message: 'A later challenge' }),
    /ended|active|SESSION/i,
  )
  assert.equal(
    await withPrfAccount(new Uint8Array(32), async (a) => a.address),
    address,
  )
  assert.notEqual(
    await withPrfAccount(new Uint8Array(32).fill(7), async (a) => a.address),
    address,
  )
})
test('wrong passkey and callback errors never retain an active signing session', async () => {
  let called = false
  const entropy = new Uint8Array(32).fill(7)
  await assert.rejects(
    withPrfAccount(
      entropy,
      async () => {
        called = true
      },
      fixtureAccount.address,
    ),
    /different wallet/,
  )
  assert.equal(called, false)
  assert.ok(entropy.every((v) => v === 0))
  let released
  await assert.rejects(
    withPrfAccount(new Uint8Array(32), async (a) => {
      released = a
      throw Error('test failure')
    }),
    /test failure/,
  )
  await assert.rejects(
    released.signMessage({ message: 'cannot sign' }),
    /ended|active|SESSION/i,
  )
})
test('real Mera passkey ceremonies enforce user verification and only persist public metadata', async () => {
  storage.clear()
  let createRequest, getRequest
  const client = {
    createCredential: async (request) => {
      createRequest = request
      return {
        credentialId: new Uint8Array([1, 2, 3]),
        prfEnabled: true,
        prfOutput: new Uint8Array(32),
      }
    },
    getCredential: async (request) => {
      getRequest = request
      return {
        credentialId: new Uint8Array([1, 2, 3]),
        prfOutput: new Uint8Array(32),
      }
    },
  }
  assert.equal(
    await withMeraWallet(
      { create: true, webAuthnClient: client },
      async (a) => a.address,
    ),
    fixtureAccount.address,
  )
  assert.equal(createRequest.rp.id, 'localhost')
  assert.equal(createRequest.userVerification, 'required')
  assert.equal(createRequest.residentKey, 'required')
  assert.equal(createRequest.challenge.length, 32)
  const saved = JSON.parse(storage.get(meraCredentialsKey))
  assert.deepEqual(Object.keys(saved[0]).sort(), [
    'credentialId',
    'rpId',
    'wallet',
  ])
  assert.equal(saved[0].wallet, fixtureAccount.address.toLowerCase())
  await withMeraWallet(
    { expectedAddress: fixtureAccount.address, webAuthnClient: client },
    async () => {},
  )
  assert.equal(getRequest.rpId, 'localhost')
  assert.equal(getRequest.userVerification, 'required')
  assert.deepEqual([...getRequest.allowCredential.credentialId], [1, 2, 3])
  assert.deepEqual(getRequest.prfSalt, createRequest.prfSalt)
  await withMeraWallet({ webAuthnClient: client }, async () => {})
  assert.equal(
    getRequest.allowCredential,
    undefined,
    'sign-in must discover synced passkeys on a new device',
  )
})
test('cancelled or unsupported PRF ceremonies never reach authentication or signing', async () => {
  let called = false
  const callback = async () => {
    called = true
  }
  await assert.rejects(
    withMeraWallet(
      {
        webAuthnClient: {
          getCredential: async () => {
            throw new DOMException('The operation was not allowed.', 'NotAllowedError')
          },
        },
      },
      callback,
    ),
    (e) => e.code === 4001,
  )
  await assert.rejects(
    withMeraWallet(
      {
        webAuthnClient: {
          getCredential: async () => ({ credentialId: new Uint8Array([1]) }),
        },
      },
      callback,
    ),
    /PRF/,
  )
  assert.equal(called, false)
})
test('native creation errors remain actionable and never create a login or persist a wallet', async () => {
  storage.clear()
  let called = false
  for (const name of ['SecurityError', 'TypeError', 'NotSupportedError', 'NotAllowedError']) {
    await assert.rejects(
      withMeraWallet({
        create: true,
        webAuthnClient: {
          createCredential: async () => { throw new DOMException('Browser diagnostic detail', name) },
        },
      }, async () => { called = true }),
      (error) => {
        assert.match(error.message, new RegExp(`create / ${name}`))
        assert.match(error.message, /Browser diagnostic detail/)
        assert.equal(error.code === 4001, name === 'NotAllowedError')
        return true
      },
    )
  }
  assert.equal(called, false)
  assert.equal(storage.get(meraCredentialsKey), undefined)
})
test('a failed PRF follow-up tells users to recover the saved passkey instead of creating another', async () => {
  await assert.rejects(
    withMeraWallet({
      create: true,
      webAuthnClient: {
        createCredential: async () => ({ credentialId: new Uint8Array([4, 5]), prfEnabled: true }),
        getCredential: async () => { throw new DOMException('Interrupted', 'AbortError') },
      },
    }, async () => assert.fail('must not reach login')),
    /passkey was saved.*Log in with passkey/,
  )
})
const signer = privateKeyToAccount('0x' + 'ab'.repeat(32))
const operation = {
  action: 'reserve_deposit',
  amount_units: '50',
  to: '0x' + '11'.repeat(20),
  data: '0xb6b55f25' + '0'.repeat(62) + '32',
  title: 'Deposit',
  description: 'Deposit 50 units',
}
async function signed(changes = {}) {
  const raw = await signer.signTransaction({
    type: 'eip1559',
    chainId: 10143,
    nonce: 7,
    gas: 100000n,
    maxFeePerGas: 100n,
    maxPriorityFeePerGas: 1n,
    to: operation.to,
    data: operation.data,
    value: 0n,
    ...changes,
  })
  return { id: keccak256(raw), raw, account: signer.address, operation }
}
test('recovery rejects mismatched account, chain, calldata, target, value or transaction hash', async () => {
  const value = await signed()
  await validateMeraTransaction(value)
  for (const changed of [
    { ...value, account: fixtureAccount.address },
    { ...value, id: '0x' + 'aa'.repeat(32) },
    { ...value, operation: { ...operation, data: '0x' } },
    { ...value, operation: { ...operation, to: fixtureAccount.address } },
    await signed({ chainId: 1 }),
    await signed({ value: 1n }),
  ])
    await assert.rejects(validateMeraTransaction(changed), /does not match/)
})
test('mainnet passkey recovery validates chain 143 and cannot replay a testnet journal', async () => {
  storage.clear()
  const testnet = await signed()
  const mainnet = await signed({ chainId: 143 })
  configureNetwork(10143)
  storage.set(networkStorageKey('tokenswoop:mera:transaction', signer.address.toLowerCase()), JSON.stringify(testnet))
  try {
    configureNetwork(143)
    assert.equal(pendingMeraTransaction(signer.address), null)
    await validateMeraTransaction(mainnet)
    await assert.rejects(validateMeraTransaction(testnet), /does not match/)
    storage.set(networkStorageKey('tokenswoop:mera:transaction', signer.address.toLowerCase()), JSON.stringify(mainnet))
    assert.equal(pendingMeraTransaction(signer.address).id, mainnet.id)
    configureNetwork(10143)
    assert.equal(pendingMeraTransaction(signer.address).id, testnet.id)
    await assert.rejects(validateMeraTransaction(mainnet), /does not match/)
  } finally { configureNetwork(10143); storage.clear() }
})
test('interrupted broadcasts retry identical signed bytes and nonce, preserving the pending journal', async () => {
  storage.clear()
  const value = await signed()
  const key = 'tokenswoop:mera:transaction:' + signer.address.toLowerCase()
  storage.set(key, JSON.stringify(value))
  const sent = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/rpc')
    assert.equal(options.credentials, 'same-origin')
    assert.equal(options.headers['X-CSRF-Token'], 'test-session')
    const { id, method, params } = JSON.parse(options.body)
    let result = null,
      error
    if (method === 'eth_chainId') result = '0x279f'
    else if (method === 'eth_sendRawTransaction') {
      sent.push(params[0])
      error = { code: -32000, message: 'connection interrupted' }
    } else if (
      method !== 'eth_getTransactionReceipt' &&
      method !== 'eth_getTransactionByHash'
    )
      throw Error('unexpected RPC ' + method)
    return Response.json({
      jsonrpc: '2.0',
      id,
      ...(error ? { error } : { result }),
    })
  }
  try {
    await assert.rejects(resumeMeraTransaction(signer.address), /uncertain/)
    await assert.rejects(resumeMeraTransaction(signer.address), /uncertain/)
    assert.ok(sent.length >= 2)
    assert.ok(sent.every((raw) => raw === value.raw))
    assert.deepEqual(pendingMeraTransaction(signer.address), value)
    assert.equal(pendingMeraTransaction(fixtureAccount.address), null)
    locks.add(key)
    const calls = sent.length
    await assert.rejects(resumeMeraTransaction(signer.address), /another tab/)
    assert.equal(sent.length, calls)
    locks.delete(key)
    storage.set(key, 'unreadable')
    assert.throws(() => pendingMeraTransaction(signer.address), /unreadable/)
  } finally {
    globalThis.fetch = originalFetch
    storage.clear()
    locks.clear()
  }
})
