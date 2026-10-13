import { network } from './network'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { ShieldCheck } from 'lucide-react'
import { api, date, money, short, useResource } from './api'
import type { Audit, Health, Tier, Unknown, User } from './api'
import { Badge, Empty, Notice, PageTitle, State } from './ui'
import { ChannelManager, ModelManager } from './AdminCatalog'
import Operations from './Operations'
import AdminReserve from './AdminReserve'

type Tab =
  | 'health'
  | 'models'
  | 'channels'
  | 'users'
  | 'tiers'
  | 'reserve'
  | 'audit'
  | 'operations'
export default function Admin() {
  const readRoute = () => new URLSearchParams(location.hash.split('?')[1] ?? '')
  const [route, setRoute] = useState(readRoute)
  useEffect(() => {
    const update = () => setRoute(readRoute())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])
  const section = route.get('section') as Tab
  const tab: Tab = [
    'channels',
    'models',
    'health',
    'users',
    'tiers',
    'reserve',
    'audit',
    'operations',
  ].includes(section)
    ? section
    : 'channels'
  return (
    <>
      <PageTitle
        eyebrow="OPERATOR WORKSPACE"
        title="Keep things moving"
        description="Manage supply, pricing and account access. Every configuration change is audited."
        action={
          <Badge tone="lime">
            <ShieldCheck size={13} /> Administrator
          </Badge>
        }
      />
      <nav className="c-tabs" aria-label="Operations sections">
        {(
          [
            'channels',
            'models',
            'operations',
            'health',
            'users',
            'tiers',
            'reserve',
            'audit',
          ] as Tab[]
        ).map((t) => (
          <button
            className={tab === t ? 'active' : ''}
            onClick={() => {
              location.hash = `admin?section=${t}`
            }}
            key={t}
          >
            {t === 'health'
              ? 'Health & recovery'
              : t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </nav>
      {tab === 'operations' ? (
        <Operations />
      ) : tab === 'health' ? (
        <HealthPanel />
      ) : tab === 'models' ? (
        <ModelManager
          key={route.get('model') ?? ''}
          initialID={route.get('model') ?? ''}
        />
      ) : tab === 'channels' ? (
        <ChannelManager
          key={route.get('channel') ?? ''}
          initialID={route.get('channel') ?? ''}
        />
      ) : tab === 'users' ? (
        <Users />
      ) : tab === 'reserve' ? (
        <AdminReserve />
      ) : tab === 'tiers' ? (
        <Tiers />
      ) : (
        <AuditLog />
      )}
    </>
  )
}
function useAction() {
  const [error, setError] = useState(''),
    [message, setMessage] = useState(''),
    [busy, setBusy] = useState(false)
  return {
    error,
    message,
    busy,
    run: async (fn: () => Promise<void>, success = 'Saved.') => {
      setBusy(true)
      setError('')
      setMessage('')
      try {
        await fn()
        setMessage(success)
      } catch (e) {
        setError((e as Error).message)
      } finally {
        setBusy(false)
      }
    },
  }
}
function Feedback({ action }: { action: ReturnType<typeof useAction> }) {
  return (
    <>
      {action.error && <Notice error>{action.error}</Notice>}
      {action.message && <Notice>{action.message}</Notice>}
    </>
  )
}
function Field({
  name,
  label,
  value = '',
  type = 'text',
  required = true,
  ...rest
}: {
  name: string
  label: string
  value?: string | number
  type?: string
  required?: boolean
  min?: number
  max?: number
  step?: string
  placeholder?: string
  maxLength?: number
}) {
  return (
    <label>
      {label}
      <input
        name={name}
        defaultValue={value}
        type={type}
        required={required}
        {...rest}
      />
    </label>
  )
}
function values(event: FormEvent<HTMLFormElement>) {
  event.preventDefault()
  return new FormData(event.currentTarget)
}

function HealthPanel() {
  const health = useResource<Health>('/api/admin/health'),
    unknown = useResource<{ data: Unknown[] }>('/api/admin/requests/unknown'),
    action = useAction(),
    [waive, setWaive] = useState('')
  return (
    <>
      <div className="c-section-actions">
        <span className="c-muted">
          Refresh to see current operational state.
        </span>
        <button
          className="c-button c-secondary"
          onClick={() => {
            health.reload()
            unknown.reload()
          }}
        >
          Refresh health
        </button>
      </div>
      <State loading={health.loading} error={health.error} />
      {health.data && (
        <div className="c-metrics">
          {[
            {
              label: 'Database / rate limiter',
              value: health.data.redis_ready ? 'Ready' : 'Redis unavailable',
              detail: 'Gateway dependencies',
            },
            {
              label: 'Background worker',
              value: health.data.worker_ready ? 'Healthy' : 'Needs attention',
              detail: health.data.worker_last_seen
                ? `Seen ${date(health.data.worker_last_seen)}`
                : 'No heartbeat received',
            },
            {
              label: 'Wallet settlement',
              value: health.data.settlement_enabled
                ? health.data.settlement_ready
                  ? 'Online'
                  : 'Needs attention'
                : 'Disabled',
              detail: `${health.data.queued_bills} queued · ${health.data.blocked_bills} blocked · ${health.data.review_bills} review`,
            },
            {
              label: 'Channels cooling down',
              value: health.data.cooling_channels,
              detail: 'Automatically retried after cooldown',
            },
            {
              label: 'Unresolved requests',
              value: health.data.unknown_requests,
              detail: `${health.data.outbox_pending} operational events pending`,
            },
            {
              label: 'Requests · 24 hours',
              value: health.data.requests_24h,
              detail: 'All accounts and models',
            },
            {
              label: 'Failed / uncertain · 24 hours',
              value: health.data.failed_or_unknown_24h,
              detail: 'Includes unresolved upstream outcomes',
            },
            {
              label: 'Average latency · 24 hours',
              value: `${health.data.avg_latency_ms_24h} ms`,
              detail: 'Completed requests',
            },
          ].map((s) => (
            <div className="c-metric" key={s.label}>
              <span>{s.label}</span>
              <strong className="c-metric-text">{s.value}</strong>
              <small>{s.detail}</small>
            </div>
          ))}
        </div>
      )}
      <Feedback action={action} />
      <section className="c-panel">
        <div className="c-panel-title">
          <div>
            <h2>Usage reconciliation</h2>
            <p>
              Check provider records before waiving a request. Uncertain usage
              has not been charged; waiving closes it without billing.
            </p>
          </div>
        </div>
        <State loading={unknown.loading} error={unknown.error} />
        {unknown.data &&
          (unknown.data.data.length ? (
            <div className="c-table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Request / account</th>
                    <th>Model</th>
                    <th>Reason</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {unknown.data.data.map((r) => (
                    <tr key={r.id}>
                      <td>
                        <code title={r.id}>{short(r.id)}</code>
                        <small>{short(r.wallet)}</small>
                      </td>
                      <td>{r.model_id}</td>
                      <td>{r.error_code}</td>
                      <td>
                        {waive === r.id ? (
                          <div className="c-row-actions">
                            <button
                              className="c-button c-danger c-small"
                              disabled={action.busy}
                              onClick={() =>
                                void action.run(async () => {
                                  await api(
                                    `/api/admin/requests/${r.id}/waive`,
                                    { method: 'POST' },
                                  )
                                  unknown.reload()
                                  health.reload()
                                  setWaive('')
                                }, 'Request closed without a charge.')
                              }
                            >
                              Confirm no charge
                            </button>
                            <button
                              className="c-button c-quiet"
                              onClick={() => setWaive('')}
                            >
                              Cancel
                            </button>
                          </div>
                        ) : (
                          <button
                            className="c-button c-quiet"
                            onClick={() => setWaive(r.id)}
                          >
                            Waive charge
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty title="Nothing waiting for review.">
              Requests with uncertain usage will appear here.
            </Empty>
          ))}
      </section>
      <Notice>
        Wallet collections use finalized chain receipts. Failed balances or
        allowances retry automatically. Available reserve can be withdrawn
        directly without platform approval.
      </Notice>
    </>
  )
}

function Users() {
  const [search, setSearch] = useState('')
  const users = useResource<{ data: User[] }>(
      `/api/admin/users?wallet=${encodeURIComponent(search)}`,
    ),
    tiers = useResource<{ data: Tier[] }>('/api/admin/tiers'),
    action = useAction()
  return (
    <>
      <div className="c-filters">
        <label className="c-search">
          Find a wallet
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="0x…"
          />
        </label>
      </div>
      <Feedback action={action} />
      <State loading={users.loading} error={users.error || tiers.error} />
      <section className="c-panel">
        <div className="c-table-wrap">
          <table>
            <thead>
              <tr>
                <th>Wallet</th>
                <th>Role</th>
                <th>Outstanding</th>
                <th>Tier</th>
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {users.data?.data.map((u) => (
                <tr key={u.id}>
                  <td>
                    <code title={u.wallet}>{short(u.wallet)}</code>
                  </td>
                  <td>{u.role}</td>
                  <td>${money(u.unpaid_balance, 12)}</td>
                  <td>
                    <select
                      aria-label={`Tier for ${u.wallet}`}
                      value={u.tier_override ? u.tier : 'auto'}
                      disabled={action.busy}
                      onChange={(e) =>
                        void action.run(async () => {
                          await api(`/api/admin/users/${u.id}`, {
                            method: 'PATCH',
                            body: JSON.stringify({
                              status: u.status,
                              tier:
                                e.target.value === 'auto'
                                  ? u.tier
                                  : e.target.value,
                              tier_mode:
                                e.target.value === 'auto' ? 'auto' : 'manual',
                            }),
                          })
                          users.reload()
                        })
                      }
                    >
                      <option value="auto">Automatic · {u.tier}</option>
                      {tiers.data?.data.map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.id}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <Badge tone={u.status === 'active' ? 'lime' : 'amber'}>
                      {u.status}
                    </Badge>
                  </td>
                  <td>
                    <button
                      className="c-button c-quiet"
                      disabled={action.busy}
                      onClick={() =>
                        void action.run(async () => {
                          await api(`/api/admin/users/${u.id}`, {
                            method: 'PATCH',
                            body: JSON.stringify({
                              tier: u.tier,
                              tier_mode: u.tier_override ? 'manual' : 'auto',
                              status:
                                u.status === 'active' ? 'suspended' : 'active',
                            }),
                          })
                          users.reload()
                        })
                      }
                    >
                      {u.status === 'active' ? 'Suspend' : 'Restore'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {users.data?.data.length === 0 && (
          <Empty title="No matching accounts." />
        )}
      </section>
      <Notice>
        Automatic tiers follow confirmed Reserve principal. Choose a named tier
        to apply an operator override; choose Automatic to restore
        principal-based assignment.
      </Notice>
    </>
  )
}

function Tiers() {
  const tiers = useResource<{ data: Tier[]; max_settlement_threshold: string }>(
      '/api/admin/tiers',
    ),
    action = useAction(),
    [editing, setEditing] = useState<Tier | null>(null)
  return (
    <>
      <Feedback action={action} />
      <div className="c-tier-admin">
        <section className="c-panel">
          <div className="c-panel-title">
            <h2>Reserve tiers</h2>
            <button
              className="c-button c-quiet"
              onClick={() => setEditing(null)}
            >
              New tier
            </button>
          </div>
          <State loading={tiers.loading} error={tiers.error} />
          <div className="c-table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tier</th>
                  <th>Minimum reserve</th>
                  <th>Discount</th>
                  <th>Settle at · {network().symbol}</th>
                  <th>RPM / concurrent</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {tiers.data?.data.map((t) => (
                  <tr key={t.id}>
                    <td>{t.id}</td>
                    <td>
                      {t.min_principal === null
                        ? 'Manual only'
                        : `${money(t.min_principal, 0)} ${network().symbol}`}
                    </td>
                    <td>{t.discount_bps / 100}%</td>
                    <td>{money(t.settlement_threshold)}</td>
                    <td>
                      {t.rpm} / {t.concurrency}
                    </td>
                    <td>
                      <button
                        className="c-button c-quiet"
                        onClick={() => setEditing(t)}
                      >
                        Edit
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
        <form
          key={editing?.id ?? 'new'}
          className="c-panel c-form"
          onSubmit={(e) => {
            const f = values(e)
            void action.run(async () => {
              await api('/api/admin/tiers', {
                method: 'PUT',
                body: JSON.stringify({
                  id: f.get('id'),
                  min_principal:
                    String(f.get('min_principal') ?? '').trim() || null,
                  discount_bps: Number(f.get('discount_bps')),
                  rpm: Number(f.get('rpm')),
                  concurrency: Number(f.get('concurrency')),
                  settlement_threshold: String(
                    f.get('settlement_threshold') ?? '',
                  ).trim(),
                }),
              })
              tiers.reload()
            })
          }}
        >
          <h2>{editing ? 'Edit service tier' : 'Create service tier'}</h2>
          <Field
            name="id"
            label="Tier ID · lowercase"
            value={editing?.id}
            maxLength={32}
          />
          <Field
            name="min_principal"
            label={`Minimum reserve · ${network().symbol}`}
            value={
              editing?.min_principal ?? (editing?.id === 'free' ? '0' : '')
            }
            required={false}
            placeholder="Leave blank for manual assignment only"
            maxLength={25}
          />
          <Field
            name="discount_bps"
            label="Discount · basis points (100 = 1%)"
            value={editing?.discount_bps ?? 0}
            type="number"
            min={0}
            max={10000}
          />
          <Field
            name="settlement_threshold"
            label={`Settlement threshold · ${network().symbol}`}
            value={editing?.settlement_threshold ?? '0.01'}
            type="number"
            min={0.000001}
            max={Number(tiers.data?.max_settlement_threshold ?? 100)}
            step="0.000001"
          />
          <p className="c-muted">
            Settle at this amount or after 5 seconds, whichever comes first.
          </p>
          <div className="c-form-grid">
            <Field
              name="rpm"
              label="Requests per minute"
              value={editing?.rpm ?? 30}
              type="number"
              min={1}
              max={100000}
            />
            <Field
              name="concurrency"
              label="Concurrent requests"
              value={editing?.concurrency ?? 2}
              type="number"
              min={1}
              max={1000}
            />
          </div>
          <button className="c-button c-primary" disabled={action.busy}>
            Save tier
          </button>
        </form>
      </div>
      <Notice>
        Limits apply across every key and Playground session belonging to the
        same wallet. Only active requests occupy concurrency slots. Uncertain
        usage awaits review without holding funds.
      </Notice>
    </>
  )
}
function AuditLog() {
  const audit = useResource<{ data: Audit[] }>('/api/admin/audit')
  return (
    <section className="c-panel">
      <div className="c-panel-title">
        <div>
          <h2>Configuration audit</h2>
          <p>Latest 100 events. Credentials and prompts are excluded.</p>
        </div>
        <button className="c-button c-secondary" onClick={audit.reload}>
          Refresh
        </button>
      </div>
      <State loading={audit.loading} error={audit.error} />
      {audit.data &&
        (audit.data.data.length ? (
          <div className="c-table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Action</th>
                  <th>Target</th>
                  <th>Actor</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                {audit.data.data.map((e) => (
                  <tr key={e.id}>
                    <td>{e.action}</td>
                    <td>
                      <code title={e.target}>
                        {e.target.length > 35 ? short(e.target) : e.target}
                      </code>
                    </td>
                    <td>
                      <code title={e.actor_id}>
                        {e.actor_id ? short(e.actor_id) : 'system'}
                      </code>
                    </td>
                    <td>{date(e.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="No changes recorded yet." />
        ))}
    </section>
  )
}
