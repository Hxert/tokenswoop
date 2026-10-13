import assert from 'node:assert/strict'
import test from 'node:test'
import { consumeChatStream } from '../src/chatStream.ts'

const bytes = new TextEncoder()
const event = (content) =>
  `data: ${JSON.stringify({ choices: [{ delta: { content } }] })}\n\n`

test(
  'renders a delta before completion and stops at DONE without waiting for EOF',
  { timeout: 2000 },
  async () => {
    let source,
      cancelled = false
    const first = Promise.withResolvers()
    const output = []
    const stream = new ReadableStream({
      start(c) {
        source = c
      },
      cancel() {
        cancelled = true
      },
    })
    const running = consumeChatStream(stream, (delta) => {
      output.push(delta)
      first.resolve()
    })
    source.enqueue(bytes.encode(event('First')))
    await first.promise
    assert.deepEqual(output, [{ content: 'First', reasoning: '' }])
    assert.equal(cancelled, false)
    source.enqueue(bytes.encode(event(' second') + 'data: [DONE]\n\n'))
    await running
    assert.equal(output.map((d) => d.content).join(''), 'First second')
    assert.equal(cancelled, true)
  },
)

test('handles split UTF-8, split CRLF, comments, reasoning, multiline data and usage-only events', async () => {
  const data =
    ': keepalive\r\n\r\n' +
    'data: {"choices":[{"delta":{"reasoning_content":"思考"}}]}\r\n\r\n' +
    'event: message\r\ndata: {"choices":\r\ndata: [{"delta":{"content":"你好 🌍"}}]}\r\n\r\n' +
    'data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}\r\n\r\n' +
    'data: [DONE]\r\n\r\n'
  const output = []
  await consumeChatStream(
    new ReadableStream({
      start(c) {
        for (const byte of bytes.encode(data)) c.enqueue(new Uint8Array([byte]))
        c.close()
      },
    }),
    (delta) => output.push(delta),
  )
  assert.equal(output.map((d) => d.content).join(''), '你好 🌍')
  assert.equal(output.map((d) => d.reasoning).join(''), '思考')
})

test('preserves partial output and reports missing completion or upstream errors', async () => {
  for (const suffix of [
    '',
    'data: {"error":{"message":"Upstream failed"}}\n\n',
  ]) {
    const output = []
    await assert.rejects(
      consumeChatStream(
        new ReadableStream({
          start(c) {
            c.enqueue(bytes.encode(event('Partial') + suffix))
            c.close()
          },
        }),
        (delta) => output.push(delta.content),
      ),
      suffix ? /Upstream failed/ : /ended before completion/,
    )
    assert.deepEqual(output, ['Partial'])
  }
})

test('propagates cancellation and releases its reader', async () => {
  let source
  const stream = new ReadableStream({
    start(c) {
      source = c
    },
  })
  const running = consumeChatStream(stream, () => {})
  source.error(new DOMException('Stopped', 'AbortError'))
  await assert.rejects(running, { name: 'AbortError' })
  assert.equal(stream.locked, false)
})

test('silently replaces interrupted content and reasoning on a Playground retry', async () => {
  let content = '', reasoning = '', restarts = 0
  const data =
    event('Discard this partial answer') +
    'data: {"choices":[{"delta":{"reasoning_content":"old reasoning"}}]}\n\n' +
    'event: tokenswoop.retry\r\ndata: {"type":"tokenswoop.retry"}\r\n\r\n' +
    event('The complete answer') + 'data: [DONE]\n\n'
  await consumeChatStream(new ReadableStream({
    start(c) {
      for (const byte of bytes.encode(data)) c.enqueue(new Uint8Array([byte]))
      c.close()
    },
  }), delta => {
    content += delta.content
    reasoning += delta.reasoning
  }, () => {
    content = ''
    reasoning = ''
    restarts++
  })
  assert.equal(content, 'The complete answer')
  assert.equal(reasoning, '')
  assert.equal(restarts, 1)
})

test('requires an explicit restart handler and ignores unframed upstream reset lookalikes', async () => {
  const stream = text => new ReadableStream({start(c) { c.enqueue(bytes.encode(text)); c.close() }})
  await assert.rejects(consumeChatStream(stream(
    'event: tokenswoop.retry\ndata: {"type":"tokenswoop.retry"}\n\n'
  ), () => {}), /restarted unexpectedly/)
  let restarts = 0
  await consumeChatStream(stream(
    'data: {"type":"tokenswoop.retry"}\n\n' +
    'event: tokenswoop.retry\n\n' +
    'data: {"type":"tokenswoop.retry"}\n\n' +
    'data: [DONE]\n\n'
  ), () => {}, () => { restarts++ })
  assert.equal(restarts, 0)
})
