export type ChatDelta = { content: string; reasoning: string }

// SSE lines and UTF-8 characters may span any number of network chunks.
export async function consumeChatStream(
  stream: ReadableStream<Uint8Array>,
  onDelta: (delta: ChatDelta) => void,
  onRestart?: () => void,
) {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let data: string[] = []
  let eventName = ''
  let complete = false
  const dispatch = () => {
    const name = eventName
    eventName = ''
    if (!data.length || complete) return
    const payload = data.join('\n')
    data = []
    if (payload.trim() === '[DONE]') {
      complete = true
      return
    }
    const event = JSON.parse(payload)
    if (name === 'tokenswoop.retry' && event.type === 'tokenswoop.retry') {
      if (!onRestart) throw new Error('The stream restarted unexpectedly.')
      onRestart()
      return
    }
    if (event.error)
      throw new Error(event.error.message || 'The stream failed.')
    const delta = event.choices?.[0]?.delta
    if (!delta) return
    const content =
      typeof delta.content === 'string'
        ? delta.content
        : Array.isArray(delta.content)
          ? delta.content
              .map((part: { text?: string }) => part.text ?? '')
              .join('')
          : typeof delta.refusal === 'string'
            ? delta.refusal
            : ''
    const reasoning = delta.reasoning_content ?? delta.reasoning ?? ''
    if (content || (typeof reasoning === 'string' && reasoning)) {
      onDelta({
        content,
        reasoning: typeof reasoning === 'string' ? reasoning : '',
      })
    }
  }
  const line = (value: string) => {
    if (value === '') dispatch()
    else if (value.startsWith('event:'))
      eventName = value.slice(6).replace(/^ /, '')
    else if (value === 'data') data.push('')
    else if (value.startsWith('data:'))
      data.push(value.slice(5).replace(/^ /, ''))
  }
  const drain = (eof = false) => {
    while (!complete) {
      const match = /[\r\n]/.exec(buffer)
      if (!match) break
      const index = match.index
      if (buffer[index] === '\r' && index === buffer.length - 1 && !eof) break
      const value = buffer.slice(0, index)
      const length =
        buffer[index] === '\r' && buffer[index + 1] === '\n' ? 2 : 1
      buffer = buffer.slice(index + length)
      line(value)
    }
    if (eof && !complete) {
      if (buffer) line(buffer)
      buffer = ''
      dispatch()
    }
  }
  try {
    while (!complete) {
      const chunk = await reader.read()
      buffer += decoder.decode(chunk.value, { stream: !chunk.done })
      drain(chunk.done)
      if (chunk.done) break
    }
    if (!complete)
      throw new Error(
        'The stream ended before completion. Check Usage for the request status.',
      )
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
