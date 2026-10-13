import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import {
  keccak256,
  toHex,
  recoverTypedDataAddress,
  recoverMessageAddress,
} from 'viem'
import { recoverAuthorizationAddress } from 'viem/utils'
import { withPrfAccount } from '../src/meraWallet.ts'
import { configureNetwork, networkStorageKey } from '../src/network.ts'
import {
  meraTypedData,
  validateMeraOperation,
  restoreSponsoredMera,
  sponsoredMeraPending,
  resumeSponsoredMera,
  sendSponsoredMera,
  watchSponsoredMera,
} from '../src/meraSponsorship.ts'

// Keep the real polling path while advancing only its 1.5-second delays.
const realTimeout = globalThis.setTimeout
mock.method(globalThis, 'setTimeout', (fn, delay, ...args) =>
  realTimeout(fn, delay === 1500 ? 0 : delay, ...args),
)
test.after(() => mock.restoreAll())

const storage = new Map(),
  held = new Set()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k) => storage.get(k) ?? null,
    setItem: (k, v) => storage.set(k, v),
    removeItem: (k) => storage.delete(k),
  },
})
Object.defineProperty(globalThis, 'document', {
  configurable: true,
  value: { cookie: 'ts_csrf=test' },
})
Object.defineProperty(globalThis.navigator, 'locks', {
  configurable: true,
  value: {
    request: async (k, opts, run) => {
      if (held.has(k)) return run(null)
      held.add(k)
      try {
        return await run({})
      } finally {
        held.delete(k)
      }
    },
  },
})
const config = {
  gas_sponsorship: true,
  implementation: '0x1111111111111111111111111111111111111111',
  token: '0x2222222222222222222222222222222222222222',
  payment: '0x3333333333333333333333333333333333333333',
  reserve: '0x4444444444444444444444444444444444444444',
}
const wallet = await withPrfAccount(new Uint8Array(32).fill(13), (a) =>
  a.address.toLowerCase(),
)
function fixture(chain = 143) {
  configureNetwork(chain)
  const id = '12345678-1234-1234-1234-123456789abc'
  const operation = {
    action: 'reserve_deposit',
    amount_units: '10000',
    to: config.reserve,
    data: '0xb6b55f25' + 10000n.toString(16).padStart(64, '0'),
    title: 'Add reserve',
    description: '0.01 USDC',
  }
  const o = {
    id,
    wallet,
    chain_id: chain,
    implementation: config.implementation,
    action: operation.action,
    amount_units: operation.amount_units,
    operation_id: keccak256(toHex(`mera:${chain}:${wallet}:${id}`)),
    deadline: Math.floor(Date.now() / 1000) + 600,
    authorization_nonce: 0,
    state: 'prepared',
    hash: '',
    error_code: '',
  }
  return { o, journal: { id, account: wallet, operation } }
}
test('Mera SDK signs chain-bound authorization and exact operation for both networks', async () => {
  for (const chain of [143, 10143]) {
    const { o, journal } = fixture(chain)
    validateMeraOperation(o, journal, config)
    await withPrfAccount(new Uint8Array(32).fill(13), async (a) => {
      const authorization = await a.signAuthorization({
        address: config.implementation,
        chainId: chain,
        nonce: 0,
      })
      assert.equal(
        (await recoverAuthorizationAddress({ authorization })).toLowerCase(),
        wallet,
      )
      const signature = await a.signTypedData(meraTypedData(o))
      assert.equal(
        (
          await recoverTypedDataAddress({ ...meraTypedData(o), signature })
        ).toLowerCase(),
        wallet,
      )
      assert.notEqual(
        (
          await recoverTypedDataAddress({
            ...meraTypedData({ ...o, chain_id: chain === 143 ? 10143 : 143 }),
            signature,
          })
        ).toLowerCase(),
        wallet,
      )
    })
  }
})
test('authorization validation rejects changed chain, target, action, amount, ID and expiry', () => {
  const { o, journal } = fixture()
  for (const patch of [
    { chain_id: 10143 },
    { amount_units: '20000' },
    { action: 'reserve_withdraw' },
    { implementation: config.payment },
    { operation_id: '0x' + 'f'.repeat(64) },
    { deadline: 1 },
    { authorization_nonce: -1 },
  ])
    assert.throws(() =>
      validateMeraOperation({ ...o, ...patch }, journal, config),
    )
  assert.throws(() =>
    validateMeraOperation(
      o,
      { ...journal, operation: { ...journal.operation, to: config.payment } },
      config,
    ),
  )
  assert.throws(() =>
    validateMeraOperation(
      o,
      { ...journal, operation: { ...journal.operation, data: '0x' } },
      config,
    ),
  )
})
test('server recovery restores a lost browser journal and resumes the same submitted operation', async () => {
  storage.clear()
  const { o } = fixture()
  const hash = '0x' + 'a'.repeat(64)
  const calls = []
  globalThis.fetch = async (route, init) => {
    calls.push({ route, body: init?.body })
    return Response.json({
      ...o,
      state: route.endsWith('/pending') ? 'submitted' : 'confirmed',
      hash,
    })
  }
  await restoreSponsoredMera(wallet, config)
  assert.equal(sponsoredMeraPending(wallet).id, o.id)
  assert.equal(await resumeSponsoredMera(wallet, config, async () => {}), hash)
  assert.equal(calls.at(-1).route, `/api/wallet/mera/transactions/${o.id}`)
  assert.ok(calls.every((call) => call.body === undefined))
  assert.ok(
    ![...storage.values()].some(
      (v) => v.includes('signature') || v.includes('privateKey'),
    ),
  )
  configureNetwork(10143)
  assert.equal(sponsoredMeraPending(wallet), null)
  configureNetwork(143)
  globalThis.fetch = async () => Response.json(null)
  await restoreSponsoredMera(wallet, config)
  assert.equal(sponsoredMeraPending(wallet), null)
})
test('background recovery shares the legacy transaction lock and cannot clear an in-flight operation', async () => {
  const { o } = fixture()
  let called = false
  const lock = networkStorageKey('tokenswoop:mera:transaction', wallet)
  held.add(lock)
  globalThis.fetch = async () => {
    called = true
    return Response.json(o)
  }
  await assert.rejects(restoreSponsoredMera(wallet, config), /other tab/)
  assert.equal(called, false)
  held.delete(lock)
})
test('expired or rejected operations release browser state instead of trapping the wallet', async () => {
  const { o } = fixture()
  globalThis.fetch = async () => Response.json({ ...o, state: 'prepared' })
  await restoreSponsoredMera(wallet, config)
  globalThis.fetch = async () => Response.json({ ...o, state: 'rejected' })
  await assert.rejects(
    resumeSponsoredMera(wallet, config, async () => {}),
    /expired or was rejected/,
  )
  assert.equal(sponsoredMeraPending(wallet), null)
})

// Exercise the real Mera browser adapter and SDK signing, with an authenticator
// fixture. These tests never contact an RPC, use a real passkey or broadcast.
function staleSessionFixture({
  source = 'external',
  cancel = false,
  wrongWallet = false,
  staleAtSubmit = false,
  ambiguousSubmit = false,
  unavailable = false,
} = {}) {
  storage.clear()
  const { o: template, journal } = fixture()
  const state = {
    prompts: 0,
    logins: 0,
    prepares: [],
    submissions: [],
    operation: null,
    source,
  }
  const hash = '0x' + 'b'.repeat(64)
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { isSecureContext: true },
  })
  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: { hostname: 'localhost' },
  })
  Object.defineProperty(globalThis, 'PublicKeyCredential', {
    configurable: true,
    value: class {},
  })
  Object.defineProperty(globalThis.navigator, 'credentials', {
    configurable: true,
    value: {
      get: async ({ publicKey }) => {
        state.prompts++
        assert.equal(publicKey.userVerification, 'required')
        assert.equal(publicKey.rpId, 'localhost')
        if (cancel) throw new DOMException('User cancelled', 'NotAllowedError')
        return {
          type: 'public-key',
          rawId: new Uint8Array([1, 2, 3]).buffer,
          getClientExtensionResults: () => ({
            prf: {
              results: {
                first: new Uint8Array(32).fill(wrongWallet ? 7 : 13).buffer,
              },
            },
          }),
        }
      },
    },
  })
  document.cookie = 'ts_csrf=old-session'
  const requiresMera = () =>
    Response.json(
      {
        error: {
          code: 'mera_wallet_required',
          message: 'Sign in with your Mera passkey to use gas sponsorship.',
        },
      },
      { status: 409 },
    )
  globalThis.fetch = async (route, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : null
    if (route === '/api/auth/challenge') {
      assert.equal(body.source, 'mera')
      assert.equal(body.wallet.toLowerCase(), wallet)
      return Response.json({
        id: 'mera-session-challenge',
        message: 'Renew this Mera wallet session',
      })
    }
    if (route === '/api/auth/login') {
      assert.equal(body.id, 'mera-session-challenge')
      assert.equal(
        (
          await recoverMessageAddress({
            message: 'Renew this Mera wallet session',
            signature: body.signature,
          })
        ).toLowerCase(),
        wallet,
      )
      state.logins++
      state.source = 'mera'
      document.cookie = 'ts_csrf=renewed-session'
      return Response.json({ id: 'same-user', wallet })
    }
    if (route === '/api/wallet/mera/transactions') {
      state.prepares.push(body)
      if (unavailable)
        return Response.json(
          { error: { code: 'mera_unavailable', message: 'Temporary failure' } },
          { status: 503 },
        )
      if (state.source !== 'mera') return requiresMera()
      state.operation ??= {
        ...template,
        id: body.id,
        action: body.action,
        amount_units: body.amount_units,
        operation_id: keccak256(toHex(`mera:143:${wallet}:${body.id}`)),
      }
      return Response.json(state.operation)
    }
    if (route.endsWith('/submit')) {
      state.submissions.push({
        route,
        body,
        csrf: init.headers['X-CSRF-Token'],
      })
      if (staleAtSubmit && state.submissions.length === 1)
        state.source = 'external'
      if (state.source !== 'mera') return requiresMera()
      assert.equal(
        (
          await recoverTypedDataAddress({
            ...meraTypedData(state.operation),
            signature: body.signature,
          })
        ).toLowerCase(),
        wallet,
      )
      assert.equal(
        (
          await recoverAuthorizationAddress({
            authorization: body.authorization,
          })
        ).toLowerCase(),
        wallet,
      )
      state.operation = { ...state.operation, state: 'submitted', hash }
      if (ambiguousSubmit && state.submissions.length === 1)
        throw new TypeError('Connection lost after submission')
      return Response.json(state.operation)
    }
    if (route === '/api/wallet/mera/pending')
      return Response.json(state.operation)
    if (route.startsWith('/api/wallet/mera/transactions/')) {
      if (init.method === 'DELETE') {
        if (state.source !== 'mera') return requiresMera()
        state.operation = null
        return new Response(null, { status: 204 })
      }
      if (state.operation?.state === 'submitted')
        state.operation.state = 'confirmed'
      return state.operation
        ? Response.json(state.operation)
        : Response.json(
            { error: { code: 'transaction_not_found' } },
            { status: 404 },
          )
    }
    throw new Error('Unexpected fixture request: ' + route)
  }
  return { state, operation: journal.operation, hash }
}

test('an old session triggers one passkey prompt, renews the same wallet and continues the original action', async () => {
  const { state, operation, hash } = staleSessionFixture()
  assert.equal(
    await sendSponsoredMera(wallet, operation, config, async () => {}),
    hash,
  )
  assert.equal(state.prompts, 1)
  assert.equal(state.logins, 1)
  assert.equal(state.prepares.length, 2)
  assert.deepEqual(
    state.prepares[0],
    state.prepares[1],
    'reauthentication must retain the original ID and amount',
  )
  assert.equal(state.submissions.length, 1)
  assert.equal(state.submissions[0].csrf, 'renewed-session')
  assert.equal(sponsoredMeraPending(wallet), null)
  assert.ok(
    ![...storage.values()].some(
      (v) => v.includes('signature') || v.includes('privateKey'),
    ),
  )
})

test('a current Mera session requires only operation confirmation, without another login', async () => {
  const { state, operation, hash } = staleSessionFixture({ source: 'mera' })
  assert.equal(
    await sendSponsoredMera(wallet, operation, config, async () => {}),
    hash,
  )
  assert.equal(state.prompts, 1)
  assert.equal(state.logins, 0)
  assert.equal(state.prepares.length, 1)
  assert.equal(state.submissions.length, 1)
})

test('a restored draft renews its old session using the same unlock and exact signed operation', async () => {
  const { state, hash } = staleSessionFixture({
    source: 'mera',
    staleAtSubmit: true,
  })
  const { o } = fixture()
  state.operation = o
  await restoreSponsoredMera(wallet, config)
  assert.equal(await resumeSponsoredMera(wallet, config, async () => {}), hash)
  assert.equal(state.prompts, 1)
  assert.equal(state.logins, 1)
  assert.equal(state.prepares.length, 0)
  assert.equal(state.submissions.length, 2)
  assert.equal(state.submissions[0].route, state.submissions[1].route)
  assert.deepEqual(state.submissions[0].body, state.submissions[1].body)
})

test('cancelled or wrong passkeys cannot renew the session or submit a transaction', async () => {
  for (const scenario of [{ cancel: true }, { wrongWallet: true }]) {
    const { state, operation } = staleSessionFixture(scenario)
    await assert.rejects(
      sendSponsoredMera(wallet, operation, config, async () => {}),
      scenario.cancel ? /cancelled/ : /different wallet/,
    )
    assert.equal(state.prompts, 1)
    assert.equal(state.logins, 0)
    assert.equal(state.submissions.length, 0)
    assert.equal(state.operation, null)
    if (scenario.cancel) assert.equal(sponsoredMeraPending(wallet), null)
  }
})

test('wallet changes during passkey verification stop automatic reauthentication', async () => {
  const { state, operation } = staleSessionFixture()
  await assert.rejects(
    sendSponsoredMera(wallet, operation, config, async () => {
      if (state.prompts) throw new Error('The signed-in wallet has changed')
    }),
    /wallet has changed/,
  )
  assert.equal(state.logins, 0)
  assert.equal(state.submissions.length, 0)
})

test('network failures do not trigger session renewal, and an ambiguous submission automatically recovers without signing twice', async () => {
  let f = staleSessionFixture({ unavailable: true })
  await assert.rejects(
    sendSponsoredMera(wallet, f.operation, config, async () => {}),
    /Temporary failure/,
  )
  assert.equal(f.state.prompts, 0)
  assert.equal(f.state.logins, 0)

  f = staleSessionFixture({ ambiguousSubmit: true })
  assert.equal(
    await sendSponsoredMera(wallet, f.operation, config, async () => {}),
    f.hash,
  )
  assert.equal(sponsoredMeraPending(wallet), null)
  assert.equal(f.state.prompts, 1)
  assert.equal(f.state.logins, 1)
  assert.equal(f.state.submissions.length, 1)
})

test('waits beyond the old polling limit and through temporary failures, without resolving at a transaction hash', async () => {
  const f = staleSessionFixture({ source: 'mera' })
  const fetch = globalThis.fetch
  let reads = 0,
    resolved = false
  globalThis.fetch = async (route, init) => {
    if (route.startsWith('/api/wallet/mera/transactions/') && !init?.method) {
      reads++
      assert.equal(resolved, false)
      assert.equal(sponsoredMeraPending(wallet).id, f.state.operation.id)
      if (reads === 3) throw new TypeError('Temporary disconnect')
      if (reads === 7)
        return Response.json(
          { error: { code: 'busy', message: 'Try later' } },
          { status: 503 },
        )
      if (reads <= 40)
        return Response.json({
          ...f.state.operation,
          state: reads < 25 ? 'queued' : 'submitted',
          hash: reads < 25 ? '' : f.hash,
        })
    }
    return fetch(route, init)
  }
  assert.equal(
    await sendSponsoredMera(wallet, f.operation, config, async () => {}).then(
      (hash) => {
        resolved = true
        return hash
      },
    ),
    f.hash,
  )
  assert.equal(reads, 41)
  assert.equal(f.state.prompts, 1)
  assert.equal(f.state.submissions.length, 1)
  assert.equal(sponsoredMeraPending(wallet), null)
})

test('refresh recovery preserves a completed result and reads without prompting or submitting', async () => {
  storage.clear()
  const { o, journal } = fixture()
  const key = networkStorageKey('tokenswoop:mera:sponsored:v1', wallet)
  storage.set(key, JSON.stringify(journal))
  let reads = 0
  const hash = '0x' + 'c'.repeat(64)
  globalThis.fetch = async (route, init) => {
    assert.equal(init?.method, undefined)
    assert.equal(route, `/api/wallet/mera/transactions/${o.id}`)
    reads++
    return Response.json({ ...o, state: 'confirmed', hash })
  }
  const restored = await restoreSponsoredMera(wallet, config)
  assert.equal(restored.state, 'confirmed')
  assert.equal(sponsoredMeraPending(wallet).id, journal.id)
  assert.equal(await watchSponsoredMera(wallet), hash)
  assert.equal(reads, 2)
  assert.equal(sponsoredMeraPending(wallet), null)
})

test('background recovery leaves unsigned drafts alone and cancellation preserves a submitted operation', async () => {
  storage.clear()
  const { o } = fixture()
  globalThis.fetch = async () => Response.json(o)
  await restoreSponsoredMera(wallet, config)
  assert.equal(await watchSponsoredMera(wallet), null)
  assert.equal(sponsoredMeraPending(wallet).id, o.id)
  const controller = new AbortController()
  globalThis.fetch = async () => {
    controller.abort()
    return Response.json({ ...o, state: 'submitted' })
  }
  await assert.rejects(watchSponsoredMera(wallet, controller.signal), {
    name: 'AbortError',
  })
  assert.equal(sponsoredMeraPending(wallet).id, o.id)
})

test('definitive balance rejection releases the journal and reports the real reason', async () => {
  storage.clear()
  const { o } = fixture()
  globalThis.fetch = async () => Response.json(o)
  await restoreSponsoredMera(wallet, config)
  globalThis.fetch = async () =>
    Response.json({
      ...o,
      state: 'rejected',
      error_code: 'insufficient_wallet_balance',
    })
  await assert.rejects(
    watchSponsoredMera(wallet),
    (e) =>
      e.terminal &&
      e.code === 'insufficient_wallet_balance' &&
      /balance/.test(e.message),
  )
  assert.equal(sponsoredMeraPending(wallet), null)
})
