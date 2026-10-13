import { useState } from 'react'
import type { FormEvent } from 'react'
import { api, date, money, short, useResource } from './api'
import type { Channel, Model, PricingTrace } from './api'
import { Badge, Empty, Notice, State } from './ui'
import { PriceTrace } from './Pricing'

type Log = {
  id: string
  user_id: string
  wallet: string
  key_id: string | null
  channel_id: string | null
  channel_name: string | null
  model_id: string
  state: string
  operation: string
  protocol: string
  upstream_protocol: string
  stream: boolean
  input_tokens: number
  output_tokens: number
  latency_ms: number | null
  first_token_ms: number | null
  generation_ms: number | null
  tokens_per_second: number | null
  amount: string
  error_code: string
  status_code: number
  created_at: string
  pricing_trace?: PricingTrace | null
  price_snapshot?: unknown
  usage_source?: string
}
type Result = {
  data: Log[]
  summary: {
    requests: number
    completed: number
    failed: number
    unknown: number
    amount: string
    input_tokens: number
    output_tokens: number
    avg_latency_ms: number | null
    p95_latency_ms: number | null
    avg_ttft_ms: number | null
    p95_ttft_ms: number | null
    tokens_per_second: number | null
  }
  daily: {
    date: string
    requests: number
    completed: number
    amount: string
    avg_ttft_ms: number | null
  }[]
  has_more: boolean
  instances: { id: string; role: string; ready: boolean; last_seen: string }[]
}
type Task = {
  id: string
  kind: string
  state: string
  processed: number
  error_code: string
  created_at: string
  finished_at: string | null
  parameters: { retain_days: number }
}
const ms = (n: number | null | undefined) =>
  n == null ? '—' : `${Math.round(n).toLocaleString()} ms`
export default function Operations() {
  const [query, setQuery] = useState(''),
    [offset, setOffset] = useState(0),
    [selected, setSelected] = useState(''),
    [retention, setRetention] = useState(30),
    [error, setError] = useState(''),
    [message, setMessage] = useState(''),
    [busy, setBusy] = useState(false)
  const logs = useResource<Result>(
      `/api/admin/operations?${query}&offset=${offset}`,
      true,
      true,
    ),
    models = useResource<{ data: Model[] }>('/api/admin/models'),
    channels = useResource<{ data: Channel[] }>('/api/admin/channels'),
    tasks = useResource<{ data: Task[] }>(
      '/api/admin/operations/tasks',
      true,
      true,
    )
  const detail = useResource<{
    request: Log
    attempts: {
      channel_name: string
      status: number
      outcome: string
      diagnostics: string[]
      created_at: string
    }[]
  }>(`/api/admin/operations/requests/${selected}`, !!selected)
  function filter(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    const p = new URLSearchParams()
    for (const [key, value] of f.entries()) {
      if (value) {
        p.set(
          key,
          key === 'from' || key === 'to' ? `${value}T00:00:00Z` : String(value),
        )
      }
    }
    setQuery(p.toString())
    setOffset(0)
    setSelected('')
  }
  const s = logs.data?.summary
  return (
    <>
      <div className="c-section-actions">
        <p className="c-muted">
          All accounts · confirmed usage, channel diagnostics and streaming
          performance. Request bodies and credentials are never logged.
        </p>
        <button
          className="c-button c-secondary"
          onClick={() => {
            logs.reload()
            tasks.reload()
          }}
        >
          Refresh
        </button>
      </div>
      {s && (
        <>
          <div className="c-metrics">
            <div className="c-metric">
              <span>REQUESTS / SUCCESS</span>
              <strong>{s.requests.toLocaleString()}</strong>
              <small>
                {s.requests
                  ? ((s.completed / s.requests) * 100).toFixed(1)
                  : '0'}
                % completed · {s.unknown} awaiting usage review
              </small>
            </div>
            <div className="c-metric">
              <span>CONFIRMED REVENUE</span>
              <strong>${money(s.amount, 6)}</strong>
              <small>
                {s.input_tokens.toLocaleString()} input /{' '}
                {s.output_tokens.toLocaleString()} output tokens
              </small>
            </div>
            <div className="c-metric">
              <span>FIRST TOKEN · STREAMS</span>
              <strong className="c-metric-text">{ms(s.avg_ttft_ms)}</strong>
              <small>P95 {ms(s.p95_ttft_ms)} · heartbeats excluded</small>
            </div>
            <div className="c-metric">
              <span>GENERATION SPEED</span>
              <strong className="c-metric-text">
                {s.tokens_per_second == null
                  ? '—'
                  : `${s.tokens_per_second} tok/s`}
              </strong>
              <small>Confirmed output / generation time</small>
            </div>
          </div>
          <p className="c-muted">
            Total latency: average {ms(s.avg_latency_ms)} · P95{' '}
            {ms(s.p95_latency_ms)}. TTFT starts at request arrival; generation
            time runs from first model content to upstream completion.
            Non-streaming calls have no TTFT or generation speed.
          </p>
        </>
      )}
      <form className="c-panel c-form" onSubmit={filter}>
        <div className="c-form-grid">
          <label>
            From · UTC
            <input name="from" type="date" />
          </label>
          <label>
            To · UTC, exclusive
            <input name="to" type="date" />
          </label>
          <label>
            Model
            <select name="model">
              <option value="">All models</option>
              {models.data?.data.map((m) => (
                <option key={m.id}>{m.id}</option>
              ))}
            </select>
          </label>
          <label>
            Channel
            <select name="channel">
              <option value="">All channels</option>
              {channels.data?.data.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Account · wallet or ID
            <input name="user" placeholder="0x… or account UUID" />
          </label>
          <label>
            API key ID
            <input name="key" placeholder="Key UUID" />
          </label>
          <label>
            State
            <select name="state">
              <option value="">All states</option>
              {[
                'completed',
                'failed',
                'usage_unknown',
                'accepted',
                'in_flight',
              ].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
          <label>
            Client protocol
            <select name="protocol">
              <option value="">All protocols</option>
              {['openai', 'openai_responses', 'claude', 'gemini'].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
          <label>
            Request ID
            <input name="request" placeholder="Request UUID" />
          </label>
        </div>
        <button className="c-button c-primary">Apply filters</button>
      </form>
      <State loading={logs.loading} error={logs.error} />
      {logs.data && (
        <section className="c-panel">
          <div className="c-table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Request / account</th>
                  <th>Model / channel</th>
                  <th>Status</th>
                  <th>Input / output</th>
                  <th>First token / total</th>
                  <th>USDC</th>
                </tr>
              </thead>
              <tbody>
                {logs.data.data.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <button
                        className="c-button c-quiet c-small"
                        onClick={() => setSelected(r.id)}
                      >
                        {short(r.id)}
                      </button>
                      <small>{date(r.created_at)}</small>
                      <small>{short(r.wallet)}</small>
                    </td>
                    <td>
                      {r.model_id}
                      <small>
                        {r.channel_name ?? 'No channel'} ·{' '}
                        {r.protocol || 'legacy'} {r.stream ? 'SSE' : ''}
                      </small>
                    </td>
                    <td>
                      <Badge tone={r.state === 'completed' ? 'lime' : 'amber'}>
                        {r.state}
                      </Badge>
                      <small>{r.error_code || r.operation}</small>
                    </td>
                    <td>
                      {r.input_tokens} / {r.output_tokens}
                    </td>
                    <td>
                      {ms(r.first_token_ms)} / {ms(r.latency_ms)}
                      <small>
                        {r.tokens_per_second == null
                          ? '—'
                          : `${r.tokens_per_second} tok/s`}
                      </small>
                    </td>
                    <td>{money(r.amount, 10)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!logs.data.data.length && (
            <Empty title="No requests in this range">
              Usage and performance appear here after an API call.
            </Empty>
          )}
          <div className="c-row-actions c-ops-pagination">
            <button
              className="c-button c-secondary"
              disabled={!offset || logs.loading}
              onClick={() => setOffset(Math.max(0, offset - 50))}
            >
              Previous
            </button>
            <span>
              {logs.data.data.length ? offset + 1 : 0}–
              {offset + logs.data.data.length}
            </span>
            <button
              className="c-button c-secondary"
              disabled={!logs.data.has_more || logs.loading}
              onClick={() => setOffset(offset + 50)}
            >
              Next
            </button>
          </div>
        </section>
      )}
      {selected && (
        <section className="c-panel c-form">
          <div className="c-panel-title">
            <h2>Request details</h2>
            <button
              className="c-button c-quiet"
              onClick={() => setSelected('')}
            >
              Close
            </button>
          </div>
          <State loading={detail.loading} error={detail.error} />
          {detail.data && (
            <>
              <code className="c-long-id">{selected}</code>
              <dl className="c-payment-addresses">
                <dt>Route</dt>
                <dd>
                  {detail.data.request.protocol} →{' '}
                  {detail.data.request.upstream_protocol} ·{' '}
                  {detail.data.request.channel_name}
                </dd>
                <dt>Actual input / output</dt>
                <dd>
                  {detail.data.request.input_tokens} /{' '}
                  {detail.data.request.output_tokens}
                </dd>
                <dt>Usage evidence</dt>
                <dd>{detail.data.request.usage_source || 'Unconfirmed'}</dd>
              </dl>
              <PriceTrace trace={detail.data.request.pricing_trace} />
              <h3>Attempts & compatibility</h3>
              {detail.data.attempts.length ? (
                detail.data.attempts.map((a, i) => (
                  <div className="c-attempt" key={i}>
                    <p>
                      {a.channel_name} · HTTP {a.status} · {a.outcome} ·{' '}
                      {date(a.created_at)}
                    </p>
                    <p>
                      {a.diagnostics.join(' · ') ||
                        'No compatibility adjustments'}
                    </p>
                  </div>
                ))
              ) : (
                <p className="c-muted">No retained attempts.</p>
              )}
              <details>
                <summary>Frozen model price</summary>
                <pre className="c-json">
                  {JSON.stringify(detail.data.request.price_snapshot, null, 2)}
                </pre>
              </details>
            </>
          )}
        </section>
      )}
      {!!logs.data?.daily.length && (
        <section className="c-panel c-form">
          <h2>Daily traffic · UTC</h2>
          <div className="c-traffic-list">
            {logs.data.daily.map((d) => (
              <div className="c-traffic-row" key={d.date}>
                <span>{d.date}</span>
                <meter
                  aria-label={`${d.date} requests`}
                  min={0}
                  max={Math.max(
                    ...logs.data!.daily.map((day) => day.requests),
                    1,
                  )}
                  value={d.requests}
                />
                <span>
                  {d.requests} calls · ${money(d.amount, 6)}
                </span>
              </div>
            ))}
          </div>
        </section>
      )}
      <section className="c-panel c-form">
        <h2>Runtime instances</h2>
        {logs.data?.instances.length ? (
          <div className="c-row-actions c-wrap">
            {logs.data.instances.map((i) => (
              <div key={i.id}>
                <Badge tone={i.ready ? 'lime' : 'amber'}>
                  {i.role} · {i.ready ? 'online' : 'stale'}
                </Badge>
                <p className="c-muted">
                  {short(i.id)} · {date(i.last_seen)}
                </p>
              </div>
            ))}
          </div>
        ) : (
          <p className="c-muted">No recent instance heartbeat.</p>
        )}
      </section>
      <section className="c-panel c-form">
        <h2>Diagnostics retention</h2>
        <p className="c-muted">
          Queue a background task to remove old attempts for completed or failed
          requests. Financial ledgers, frozen pricing, usage totals and
          unresolved requests are retained.
        </p>
        <div className="c-row-actions c-wrap">
          <label>
            Keep recent days
            <input
              type="number"
              value={retention}
              min={7}
              max={3650}
              onChange={(e) => setRetention(Number(e.target.value))}
            />
          </label>
          <button
            className="c-button c-secondary"
            disabled={
              busy ||
              retention < 7 ||
              retention > 3650 ||
              tasks.data?.data.some(
                (t) => t.state === 'queued' || t.state === 'running',
              )
            }
            onClick={async () => {
              setBusy(true)
              setError('')
              setMessage('')
              try {
                await api('/api/admin/operations/cleanup', {
                  method: 'POST',
                  body: JSON.stringify({ retain_days: retention }),
                })
                setMessage(
                  'Cleanup queued. The worker will process it in batches.',
                )
                tasks.reload()
              } catch (e) {
                setError((e as Error).message)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Queuing…' : 'Queue diagnostics cleanup'}
          </button>
        </div>
        {error && <Notice error>{error}</Notice>}
        {message && <Notice>{message}</Notice>}
        <State loading={tasks.loading} error={tasks.error} />
        {tasks.data?.data.map((t) => (
          <div className="c-attempt" key={t.id}>
            <Badge>{t.state}</Badge> {t.processed.toLocaleString()} attempts
            removed · keep {t.parameters.retain_days} days ·{' '}
            {date(t.created_at)} {t.error_code && <code>{t.error_code}</code>}
          </div>
        ))}
      </section>
    </>
  )
}
