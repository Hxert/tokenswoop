import test from 'node:test'
import assert from 'node:assert/strict'
import { createPublicClient } from 'viem'
import { monadTestnet } from 'viem/chains'
import { chainRpcTransport } from '../src/chainRpc.ts'

test('wallet RPC uses current CSRF and same-origin session, preserving reverts without rebroadcast retries', async () => {
  const originalFetch = globalThis.fetch
  const originalDocument = globalThis.document
  globalThis.document = { cookie: 'ts_csrf=first' }
  const calls = []
  let failure = false
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/rpc')
    assert.equal(options.credentials, 'same-origin')
    const body = JSON.parse(options.body)
    calls.push({ csrf: options.headers['X-CSRF-Token'], ...body })
    return Response.json(
      failure
        ? {
            jsonrpc: '2.0',
            id: body.id,
            error: {
              code: 3,
              message: 'execution reverted',
              data: '0x12345678',
            },
          }
        : { jsonrpc: '2.0', id: body.id, result: '0x279f' },
    )
  }
  try {
    const rpc = createPublicClient({
      chain: monadTestnet,
      transport: chainRpcTransport(),
    })
    assert.equal(await rpc.getChainId(), 10143)
    globalThis.document.cookie = 'ts_csrf=second'
    assert.equal(await rpc.getChainId(), 10143)
    assert.deepEqual(
      calls.map((c) => c.csrf),
      ['first', 'second'],
    )
    failure = true
    const before = calls.length
    await assert.rejects(
      rpc.request({ method: 'eth_sendRawTransaction', params: ['0x1234'] }),
      (error) => {
        assert.match(error.message, /revert/i)
        return true
      },
    )
    assert.equal(calls.length, before + 1)
    assert.deepEqual(calls.at(-1).params, ['0x1234'])
  } finally {
    globalThis.fetch = originalFetch
    globalThis.document = originalDocument
  }
})
