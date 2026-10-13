import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowUpRight, Send, Square } from 'lucide-react'
import { api, csrf, money, useResource } from './api'
import type { Model, Usage } from './api'
import { Badge, Empty, Notice, PageTitle, State } from './ui'
import { consumeChatStream } from './chatStream'

type Message = {
  role: 'user' | 'assistant'
  content: string
  reasoning?: string
}
export function Playground({ refresh }: { refresh: () => Promise<void> }) {
  const models = useResource<{ data: Model[] }>('/api/models'),
    [model, setModel] = useState(''),
    [messages, setMessages] = useState<Message[]>([]),
    [prompt, setPrompt] = useState(''),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [receipt, setReceipt] = useState<Usage | null>(null)
  const controller = useRef<AbortController | null>(null),
    messageList = useRef<HTMLDivElement>(null),
    followOutput = useRef(true)
  const selected = model || models.data?.data[0]?.id || '',
    selectedModel = models.data?.data.find((m) => m.id === selected)
  useEffect(
    () => () => {
      controller.current?.abort()
      controller.current = null
    },
    [],
  )
  useLayoutEffect(() => {
    const list = messageList.current
    if (list && followOutput.current) list.scrollTop = list.scrollHeight
  }, [messages])
  const send = async () => {
    if (!prompt.trim() || !selected || busy || controller.current) return
    const input: Message[] = [
      ...messages
        .filter((message) => message.content)
        .map(({ role, content }) => ({ role, content })),
      { role: 'user', content: prompt.trim() },
    ]
    followOutput.current = true
    setMessages([...input, { role: 'assistant', content: '' }])
    setPrompt('')
    setBusy(true)
    setError('')
    setReceipt(null)
    const abort = new AbortController()
    controller.current = abort
    let requestID: string | null = null
    try {
      const response = await fetch('/api/playground', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() },
        body: JSON.stringify({
          model: selected,
          messages: input,
          stream: true,
          stream_options: { include_usage: true },
        }),
        signal: abort.signal,
      })
      requestID = response.headers.get('X-Request-ID')
      if (!response.ok) {
        const body = await response.json()
        throw new Error(body.error?.message ?? 'The request failed.')
      }
      if (
        !response.body ||
        !response.headers.get('Content-Type')?.includes('text/event-stream')
      )
        throw new Error('Streaming is unavailable in this browser.')
      let output = '',
        reasoning = ''
      await consumeChatStream(
        response.body,
        (delta) => {
          output += delta.content
          reasoning += delta.reasoning
          if (controller.current === abort)
            setMessages([
              ...input,
              { role: 'assistant', content: output, reasoning },
            ])
        },
        () => {
          output = ''
          reasoning = ''
          if (controller.current === abort)
            setMessages([...input, { role: 'assistant', content: '' }])
        },
      )
    } catch (e) {
      if (controller.current === abort)
        setError(
          abort.signal.aborted
            ? 'Generation stopped. Check the receipt for confirmed usage and its final charge.'
            : (e as Error).message,
        )
    } finally {
      if (requestID && controller.current === abort) {
        try {
          const row = await api<Usage>(`/api/requests/${requestID}`)
          if (controller.current === abort) setReceipt(row)
        } catch {
          /* Rejected requests have no admitted ledger entry. */
        }
      }
      if (controller.current === abort) {
        controller.current = null
        setBusy(false)
        await refresh()
      }
    }
  }
  return (
    <>
      <PageTitle
        title="Playground"
        description="Test a model with the same postpaid metering as your API calls."
      />
      <State loading={models.loading} error={models.error} />
      {models.data?.data.length === 0 ? (
        <section className="c-panel">
          <Empty title="No models available">
            Contact the platform administrator to enable model access.
          </Empty>
        </section>
      ) : (
        <div className="c-playground">
          <section className="c-panel c-chat">
            <div className="c-chat-head">
              <span className="status-dot" />
              <strong>{selectedModel?.name || 'Choose a model'}</strong>
              <button
                className="c-button c-quiet"
                disabled={busy || !messages.length}
                onClick={() => {
                  setMessages([])
                  setReceipt(null)
                  setError('')
                }}
              >
                Clear chat
              </button>
            </div>
            <div
              ref={messageList}
              className="c-messages"
              role="log"
              aria-label="Chat messages"
              aria-live="polite"
              aria-relevant="additions"
              tabIndex={0}
              onScroll={(event) => {
                const list = event.currentTarget
                followOutput.current =
                  list.scrollHeight - list.scrollTop - list.clientHeight < 64
              }}
            >
              {messages.length === 0 ? (
                <div className="c-chat-empty">
                  <span>✳</span>
                  <h2>What’s on your mind?</h2>
                  <p>Start a conversation. See what your model can do.</p>
                  <div>
                    <button
                      onClick={() =>
                        setPrompt(
                          'Explain a difficult concept using a simple analogy.',
                        )
                      }
                    >
                      Explain something simply <ArrowUpRight size={14} />
                    </button>
                    <button
                      onClick={() =>
                        setPrompt(
                          'Write a concise welcome message for a new developer API.',
                        )
                      }
                    >
                      Find the right words <ArrowUpRight size={14} />
                    </button>
                  </div>
                </div>
              ) : (
                messages.map((m, i) => (
                  <div className={`c-message ${m.role}`} key={i}>
                    <span>{m.role === 'user' ? 'YOU' : 'MODEL'}</span>
                    {m.reasoning && (
                      <details
                        className="c-reasoning"
                        open={busy && i === messages.length - 1}
                      >
                        <summary>Reasoning</summary>
                        <p>{m.reasoning}</p>
                      </details>
                    )}
                    <p>
                      {m.content ||
                        (busy && i === messages.length - 1
                          ? 'Thinking…'
                          : 'No text returned.')}
                      {busy && i === messages.length - 1 && m.content && (
                        <span className="c-stream-caret" aria-hidden="true" />
                      )}
                    </p>
                  </div>
                ))
              )}
            </div>
            {error && <Notice error>{error}</Notice>}
            <form
              className="c-composer"
              onSubmit={(e) => {
                e.preventDefault()
                void send()
              }}
            >
              <label className="sr-only" htmlFor="prompt">
                Your message
              </label>
              <textarea
                id="prompt"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                onKeyDown={(e) => {
                  if (
                    e.key === 'Enter' &&
                    !e.shiftKey &&
                    !e.nativeEvent.isComposing
                  ) {
                    e.preventDefault()
                    void send()
                  }
                }}
                placeholder="Put your next idea into words…"
                rows={2}
                maxLength={100000}
                disabled={busy}
              />
              {busy ? (
                <button
                  className="c-button c-secondary"
                  type="button"
                  onClick={() => controller.current?.abort()}
                  aria-label="Stop generation"
                >
                  <Square size={16} />
                  Stop
                </button>
              ) : (
                <button
                  className="c-button c-primary"
                  disabled={!prompt.trim() || !selected}
                  aria-label="Send message"
                >
                  <Send size={17} />
                </button>
              )}
            </form>
            <div className="c-chat-foot">
              Enter to send · Shift + Enter for a new line
              <span role="status">
                {busy ? 'Generating…' : 'Chat stays in this tab'}
              </span>
            </div>
          </section>
          <aside className="c-panel c-parameters">
            <span className="c-eyebrow">MAKE IT YOURS</span>
            <h2>Parameters</h2>
            <label>
              Model
              <select
                value={selected}
                disabled={busy}
                onChange={(e) => {
                  setModel(e.target.value)
                  setMessages([])
                  setReceipt(null)
                }}
              >
                {models.data?.data.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </label>
            <div className="c-receipt">
              <span className="c-eyebrow">LAST REQUEST</span>
              {receipt ? (
                <>
                  <Badge
                    tone={receipt.state === 'completed' ? 'lime' : 'amber'}
                  >
                    {receipt.state.replaceAll('_', ' ')}
                  </Badge>
                  <dl>
                    <div>
                      <dt>Input / output</dt>
                      <dd>
                        {receipt.input_tokens} / {receipt.output_tokens}
                      </dd>
                    </div>
                    <div>
                      <dt>Actual cost</dt>
                      <dd>${money(receipt.amount, 12)}</dd>
                    </div>
                    <div>
                      <dt>Wallet paid</dt>
                      <dd>${money(receipt.wallet_paid_amount, 12)}</dd>
                    </div>
                    <div>
                      <dt>Outstanding</dt>
                      <dd>${money(receipt.unpaid_amount, 12)}</dd>
                    </div>
                    {Number(receipt.waived_amount ?? 0) > 0 && (
                      <div>
                        <dt>Waived</dt>
                        <dd>${money(receipt.waived_amount!, 18)}</dd>
                      </div>
                    )}
                    <div>
                      <dt>Latency</dt>
                      <dd>{receipt.latency_ms} ms</dd>
                    </div>
                  </dl>
                  <code>{receipt.id}</code>
                </>
              ) : (
                <p>Usage and actual cost appear after your request.</p>
              )}
            </div>
            <p className="c-muted">
              Charges use provider-reported usage after completion and settle
              from your wallet onchain.
            </p>
          </aside>
        </div>
      )}
    </>
  )
}
