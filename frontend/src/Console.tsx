import { network } from './network'
import { PublicModelPricing } from './Pricing'
import { useEffect, useRef, useState } from 'react'
import {
  Activity,
  ArrowUpRight,
  BookOpen,
  Box,
  ChevronRight,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  Moon,
  Play,
  Shield,
  Sun,
  Terminal,
  Wallet,
  X,
} from 'lucide-react'
import { api, APIError, date, money, short, useResource } from './api'
import type { APIKey, Config, Me, Model, Stats, Usage } from './api'
import {
  Badge,
  Brand,
  CopyButton,
  CopyValue,
  Empty,
  Go,
  Loading,
  Modal,
  Notice,
  PageTitle,
  State,
} from './ui'
import { Playground } from './Workbench'
import Admin from './Admin'
import Reserve from './Reserve'
import Billing, { RequestReceipt } from './Billing'
import { modelBrand } from './brands'
import { useWorkspaceTheme } from './workspaceTheme'
import UsageChart from './UsageChart'
import { useWalletConnection } from './WalletProvider'
import WalletDetails from './WalletDetails'
import { homeHref } from './siteRouting'

const nav = [
  { id: 'overview', label: 'Overview', icon: LayoutDashboard },
  { id: 'keys', label: 'API keys', icon: KeyRound },
  { id: 'usage', label: 'Usage', icon: Activity },
  { id: 'billing', label: 'Wallet & billing', icon: Wallet },
  { id: 'reserve', label: 'Tier & reserve', icon: Shield },
  { id: 'models', label: 'Model library', icon: Box },
  { id: 'playground', label: 'Playground', icon: Play },
  { id: 'docs', label: 'Quickstart', icon: BookOpen },
]

export default function Console() {
  const wallet = useWalletConnection()
  const { theme, toggleTheme } = useWorkspaceTheme()
  const [page, setPage] = useState(
      location.hash.slice(1).split('?')[0] || 'overview',
    ),
    [mobile, setMobile] = useState(false),
    [walletDetailsOpen, setWalletDetailsOpen] = useState(false),
    [me, setMe] = useState<Me | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(''),
    [busy, setBusy] = useState(false)
  const config = useResource<Config>('/api/config')
  const refreshVersion = useRef(0)
  const refresh = async () => {
    const version = ++refreshVersion.current
    try {
      const current = await api<Me>('/api/me')
      if (version !== refreshVersion.current) return
      setMe(current)
      setError('')
    } catch (e) {
      if (version !== refreshVersion.current) return
      if (e instanceof APIError && e.status === 401) setMe(null)
      else setError((e as Error).message)
    } finally {
      if (version === refreshVersion.current) setLoading(false)
    }
  }
  useEffect(() => {
    void refresh()
    const handler = () => {
      setPage(location.hash.slice(1).split('?')[0] || 'overview')
      setMobile(false)
    }
    window.addEventListener('hashchange', handler)
    return () => window.removeEventListener('hashchange', handler)
  }, [])
  useEffect(() => {
    if (wallet.sessionVersion) {
      setMe(null)
      setLoading(true)
      void refresh()
    }
  }, [wallet.sessionVersion])
  useEffect(() => {
    setWalletDetailsOpen(false)
  }, [me?.user.id, wallet.sessionVersion])
  useEffect(() => {
    const handler = () => {
      void refresh()
    }
    window.addEventListener('tokenswoop:wallet-transaction', handler)
    return () =>
      window.removeEventListener('tokenswoop:wallet-transaction', handler)
  }, [])
  const connect = async () => {
    setWalletDetailsOpen(false)
    setBusy(true)
    setError('')
    try {
      await wallet.login()
      await refresh()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const logout = async () => {
    setWalletDetailsOpen(false)
    setBusy(true)
    try {
      await wallet.logout()
      refreshVersion.current++
      setMe(null)
      setLoading(false)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const active =
    nav.find((n) => n.id === page)?.label ??
    (page === 'admin'
      ? 'Operations'
      : page === 'receipt'
        ? 'Request receipt'
        : 'Overview')
  return (
    <div className="console" data-theme={theme}>
      <a
        className="skip-link"
        href="#workspace-main"
        onClick={(e) => {
          e.preventDefault()
          document.getElementById('workspace-main')?.focus()
        }}
      >
        Skip to content
      </a>
      <aside className={`c-sidebar ${mobile ? 'is-open' : ''}`}>
        <Brand />
        <span className="c-nav-label">WORKSPACE</span>
        <nav aria-label="Workspace">
          {nav.map(({ id, label, icon: Icon }) => (
            <a
              href={`#${id}`}
              className={page === id ? 'active' : ''}
              aria-current={page === id ? 'page' : undefined}
              key={id}
            >
              <Icon size={18} strokeWidth={1.6} />
              {label}
              {id === 'playground' && <span className="c-nav-new">TRY</span>}
            </a>
          ))}
          {me?.user.role === 'admin' && (
            <a href="#admin" className={page === 'admin' ? 'active' : ''}>
              <Shield size={18} />
              Operations
            </a>
          )}
        </nav>
        <div className="c-sidebar-bottom">
          <p>
            One API.
            <br />
            Your next big idea.
          </p>
          <a href={homeHref(window.location.hostname)}>
            Explore TokenSwoop
            <ArrowUpRight size={14} />
          </a>
        </div>
      </aside>
      <div className="c-workspace">
        <header className="c-topbar">
          <div className="c-breadcrumb">
            <button
              className="c-icon c-mobile"
              onClick={() => setMobile(!mobile)}
              aria-label="Toggle workspace navigation"
              aria-expanded={mobile}
            >
              {mobile ? <X size={21} /> : <Menu size={21} />}
            </button>
            <span>Workspace</span>
            <ChevronRight size={13} />
            <strong>{active}</strong>
          </div>
          <div className="c-account">
            <button
              type="button"
              className="c-theme-toggle"
              onClick={toggleTheme}
              aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
              title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
            >
              {theme === 'dark' ? (
                <Sun size={18} strokeWidth={1.6} aria-hidden="true" />
              ) : (
                <Moon size={18} strokeWidth={1.6} aria-hidden="true" />
              )}
            </button>
            {me ? (
              <>
                <a href="#reserve">
                  <Badge>{me.tier.id} tier</Badge>
                </a>
                <button
                  type="button"
                  className="c-wallet"
                  onClick={() => setWalletDetailsOpen(true)}
                  disabled={busy || wallet.busy}
                  title="Wallet details"
                  aria-label="Wallet details"
                  aria-haspopup="dialog"
                  aria-expanded={walletDetailsOpen}
                >
                  <Wallet size={14} />
                  {short(me.user.wallet)}
                </button>
                <button
                  className="c-icon"
                  onClick={logout}
                  disabled={busy || wallet.busy}
                  title="Sign out"
                  aria-label="Sign out"
                >
                  <LogOut size={17} />
                </button>
              </>
            ) : (
              <button
                className="c-button c-primary c-small"
                disabled={busy || loading || wallet.busy || !wallet.ready}
                onClick={connect}
              >
                <Wallet size={15} />
                {busy || wallet.busy
                  ? 'Signing in…'
                  : wallet.enabled
                    ? 'Sign in'
                    : 'Connect wallet'}
              </button>
            )}
          </div>
        </header>
        <main
          key={me?.user.id || 'signed-out'}
          className="c-main"
          id="workspace-main"
          tabIndex={-1}
        >
          {wallet.error && (
            <Notice error>
              {wallet.error}
              {!me && (
                <button
                  className="c-button c-small"
                  disabled={busy}
                  onClick={logout}
                >
                  Use another account
                </button>
              )}
            </Notice>
          )}
          {me && wallet.pending?.(me.user.wallet) && (
            <Notice>
              {wallet.busy
                ? 'Processing your wallet operation. The result will appear automatically.'
                : 'A wallet operation is pending. Resume it to continue.'}
              {!wallet.busy && (
                <button
                  className="c-button c-small"
                  disabled={busy || wallet.busy}
                  onClick={async () => {
                    setBusy(true)
                    try {
                      await wallet.resume?.(me.user.wallet)
                      await refresh()
                    } catch (e) {
                      if (!(e as { notified?: boolean }).notified)
                        setError((e as Error).message)
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  Resume transaction
                </button>
              )}
            </Notice>
          )}
          {error && (
            <Notice error>
              {error}
              <button className="c-button c-quiet" onClick={refresh}>
                Retry
              </button>
            </Notice>
          )}
          {config.error && (
            <Notice error>Cannot reach the API. {config.error}</Notice>
          )}
          {loading ? (
            <Loading />
          ) : page === 'models' ? (
            <Models />
          ) : page === 'docs' ? (
            <Quickstart />
          ) : !me ? (
            <SignIn
              busy={busy || wallet.busy || !wallet.ready}
              connect={connect}
              embedded={wallet.enabled}
            />
          ) : page === 'keys' ? (
            <Keys />
          ) : page === 'usage' ? (
            <UsagePage />
          ) : page === 'billing' ? (
            <Billing key={location.hash} me={me} refresh={refresh} />
          ) : page === 'reserve' ? (
            <Reserve me={me} refresh={refresh} />
          ) : page === 'receipt' ? (
            <RequestReceipt key={location.hash} />
          ) : page === 'playground' ? (
            <Playground refresh={refresh} />
          ) : page === 'admin' && me.user.role === 'admin' ? (
            <Admin />
          ) : (
            <Overview me={me} />
          )}
        </main>
        <footer className="c-footer">
          <span>© {new Date().getFullYear()} TokenSwoop</span>
          <span>Postpaid wallet billing · {network().name}</span>
        </footer>
      </div>
      {me && walletDetailsOpen && (
        <WalletDetails
          key={me.user.id}
          me={me}
          busy={busy || wallet.busy}
          onClose={() => setWalletDetailsOpen(false)}
          onSwitch={connect}
          onSignOut={logout}
        />
      )}
    </div>
  )
}

function SignIn({
  busy,
  connect,
  embedded,
}: {
  busy: boolean
  connect: () => void
  embedded: boolean
}) {
  return (
    <div className="c-signin">
      <div className="c-orbit-art" aria-hidden="true">
        <div />
        <div />
        <div />
        <span>↗</span>
        <b>01</b>
        <i>∞</i>
      </div>
      <Badge tone="lime">YOUR WORKSPACE AWAITS</Badge>
      <h1>
        {embedded ? 'Your workspace.' : 'Bring your wallet.'}
        <br />
        <em>Build what’s next.</em>
      </h1>
      <p>
        Your models, keys and usage, in one place.
        <br />
        {embedded
          ? 'Sign in with a passkey, email, Google or your wallet.'
          : 'Sign a message to create your TokenSwoop account.'}
      </p>
      <button className="c-button c-primary" onClick={connect} disabled={busy}>
        <Wallet size={17} />
        {busy ? 'Connecting…' : embedded ? 'Get started' : 'Connect & sign in'}
        <ArrowUpRight size={17} />
      </button>
      <small>
        {embedded
          ? 'Your selected wallet is your account.'
          : 'Sign in with your EVM wallet. No payment approval is requested.'}
      </small>
      <div className="c-signin-links">
        <a href="#models">
          Browse models <ArrowUpRight size={14} />
        </a>
        <a href="#docs">
          Read the quickstart <ArrowUpRight size={14} />
        </a>
      </div>
    </div>
  )
}

function Overview({ me }: { me: Me }) {
  const stats = useResource<Stats>('/api/stats'),
    recent = useResource<{ data: Usage[] }>('/api/usage')
  const s = stats.data?.summary
  return (
    <>
      <PageTitle
        title="Room to build"
        description="A clear view of every request, token and payment."
        action={<Go to="playground">Open Playground</Go>}
      />
      <State loading={stats.loading} error={stats.error} />
      <div className="c-metrics">
        <Metric
          label="API spend · 30 days"
          value={s ? `$${money(s.spent)}` : '—'}
        />
        <Metric
          label="Requests · 30 days"
          value={s?.requests.toLocaleString() ?? '—'}
        />
        <Metric
          label="Tokens processed"
          value={s ? (s.input_tokens + s.output_tokens).toLocaleString() : '—'}
        />
        <Metric
          label="Awaiting settlement"
          value={`$${money(me.user.unpaid_balance)}`}
        />
      </div>
      <div className="c-overview-grid">
        <section className="c-panel">
          <div className="c-panel-title">
            <div>
              <h2>Your momentum</h2>
              <p>Requests over the last 14 days · UTC</p>
            </div>
            <Badge>Live data</Badge>
          </div>
          <UsageChart daily={stats.data?.daily} />
          {s?.requests === 0 && (
            <p className="c-chart-note">Your first request starts the story.</p>
          )}
        </section>
        <section className="c-panel c-start">
          <span className="c-eyebrow">FROM ZERO TO YOUR FIRST TOKEN</span>
          <h2>
            Ready when you are<span>.</span>
          </h2>
          <a href="#billing">
            <span>01</span>
            <div>
              Set up wallet payments
              <small>Fund your wallet. Authorize a limit.</small>
            </div>
            <ArrowUpRight size={16} />
          </a>
          <a href="#keys">
            <span>02</span>
            <div>
              Create an API key<small>Set a budget. Choose your models.</small>
            </div>
            <ArrowUpRight size={16} />
          </a>
          <a href="#docs">
            <span>03</span>
            <div>
              Make something<small>One familiar, compatible API.</small>
            </div>
            <ArrowUpRight size={16} />
          </a>
        </section>
      </div>
      <section className="c-panel">
        <div className="c-panel-title">
          <div>
            <h2>Recent requests</h2>
          </div>
          <a className="c-text-link" href="#usage">
            View all <ArrowUpRight size={14} />
          </a>
        </div>
        <State loading={recent.loading} error={recent.error} />
        {recent.data && <UsageTable items={recent.data.data.slice(0, 5)} />}
      </section>
      <div className="c-account-strip">
        <Badge>{me.tier.id} tier</Badge>
        <span>{me.tier.rpm} requests / min</span>
        <span>{me.tier.concurrency} concurrent requests</span>
        <span>{me.tier.discount_bps / 100}% price discount</span>
      </div>
    </>
  )
}
function Metric({
  label,
  value,
  sub,
  accent = false,
}: {
  label: string
  value: string
  sub?: string
  accent?: boolean
}) {
  return (
    <div className={`c-metric ${accent ? 'accent' : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      {sub && <small>{sub}</small>}
    </div>
  )
}

function Keys() {
  const keys = useResource<{ data: APIKey[] }>('/api/keys'),
    models = useResource<{ data: Model[] }>('/api/models')
  const [show, setShow] = useState(false),
    [unlimited, setUnlimited] = useState(true),
    [secret, setSecret] = useState(''),
    [error, setError] = useState(''),
    [busy, setBusy] = useState(false),
    [revoke, setRevoke] = useState('')
  return (
    <>
      <PageTitle
        title="API keys"
        description="Separate your projects. Control where and how much each key can spend."
        action={
          <button
            className="c-button c-primary"
            onClick={() => {
              setShow(true)
              setUnlimited(true)
              setError('')
            }}
          >
            <KeyRound size={16} />
            Create key
          </button>
        }
      />
      {error && !show && <Notice error>{error}</Notice>}
      {(show || secret) && (
        <Modal
          title={secret ? 'Your API key' : 'Create a project key'}
          busy={busy}
          onClose={() => {
            setShow(false)
            setSecret('')
            setError('')
          }}
        >
          {secret ? (
            <section className="c-secret">
              <Badge tone="lime">COPY YOUR KEY NOW</Badge>
              <p>This is the only time the full key is available.</p>
              <div>
                <CopyValue value={secret} label="Copy API key" />
              </div>
              <button
                className="c-button c-quiet"
                onClick={() => setSecret('')}
              >
                I’ve saved my key <X size={14} />
              </button>
            </section>
          ) : (
            <form
              className="c-form c-dialog-form"
              onSubmit={async (e) => {
                e.preventDefault()
                setBusy(true)
                setError('')
                const f = new FormData(e.currentTarget)
                try {
                  const result = await api<{ key: string }>('/api/keys', {
                    method: 'POST',
                    body: JSON.stringify({
                      name: f.get('name'),
                      budget: unlimited ? null : f.get('budget'),
                      allowed_models: f.getAll('models'),
                      expires_at: f.get('expires')
                        ? new Date(String(f.get('expires'))).toISOString()
                        : null,
                    }),
                  })
                  setSecret(result.key)
                  setShow(false)
                  keys.reload()
                } catch (e) {
                  setError((e as Error).message)
                } finally {
                  setBusy(false)
                }
              }}
            >
              {error && <Notice error>{error}</Notice>}
              <div className="c-form-grid">
                <label>
                  Key name
                  <input
                    name="name"
                    data-autofocus
                    required
                    maxLength={80}
                    placeholder="My first project"
                  />
                </label>
                <label>
                  Spending limit
                  <select
                    value={unlimited ? 'unlimited' : 'limited'}
                    onChange={(e) =>
                      setUnlimited(e.target.value === 'unlimited')
                    }
                  >
                    <option value="unlimited">Unlimited</option>
                    <option value="limited">Set a limit</option>
                  </select>
                </label>
                {!unlimited && (
                  <label>
                    Lifetime limit · USDC
                    <input
                      name="budget"
                      required
                      inputMode="decimal"
                      defaultValue="1"
                      pattern="[0-9]+(\.[0-9]{1,18})?"
                    />
                  </label>
                )}
                <label>
                  Expiration · optional
                  <input name="expires" type="datetime-local" />
                </label>
              </div>
              <fieldset>
                <legend>
                  Model access · leave unchecked to allow all enabled models
                </legend>
                <State loading={models.loading} error={models.error} />
                <div className="c-checks">
                  {models.data?.data.map((m) => (
                    <label key={m.id}>
                      <input type="checkbox" name="models" value={m.id} />
                      {m.name}
                    </label>
                  ))}
                </div>
              </fieldset>
              <p className="c-muted">
                {unlimited
                  ? 'No spending cap for this key. Your account balance and payment settings still apply.'
                  : 'The limit applies to completed usage. Requests already running may finish above it.'}
              </p>
              <button className="c-button c-primary" disabled={busy}>
                {busy ? 'Creating…' : 'Create API key'}
              </button>
            </form>
          )}
        </Modal>
      )}
      <section className="c-panel">
        <State loading={keys.loading} error={keys.error} />
        {keys.data &&
          (keys.data.data.length ? (
            <div className="c-table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Name / key</th>
                    <th>Access</th>
                    <th>Spent / budget</th>
                    <th>Expires</th>
                    <th>Status</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {keys.data.data.map((k) => (
                    <tr key={k.id}>
                      <td>
                        <strong>{k.name}</strong>
                        <code>{k.prefix}••••••</code>
                      </td>
                      <td>
                        {k.allowed_models.length
                          ? k.allowed_models.join(', ')
                          : 'All models'}
                      </td>
                      <td>
                        ${money(k.spent)} /{' '}
                        {k.budget === null
                          ? 'Unlimited'
                          : `$${money(k.budget)}`}
                      </td>
                      <td>{k.expires_at ? date(k.expires_at) : 'Never'}</td>
                      <td>
                        <Badge tone={k.status === 'active' ? 'lime' : ''}>
                          {k.expires_at &&
                          new Date(k.expires_at) < new Date() &&
                          k.status === 'active'
                            ? 'expired'
                            : k.status}
                        </Badge>
                      </td>
                      <td>
                        {k.status === 'active' &&
                          (revoke === k.id ? (
                            <div className="c-row-actions">
                              <button
                                className="c-button c-danger c-small"
                                disabled={busy}
                                onClick={async () => {
                                  setBusy(true)
                                  try {
                                    await api(`/api/keys/${k.id}`, {
                                      method: 'DELETE',
                                    })
                                    keys.reload()
                                    setRevoke('')
                                  } catch (e) {
                                    setError((e as Error).message)
                                  } finally {
                                    setBusy(false)
                                  }
                                }}
                              >
                                Confirm revoke
                              </button>
                              <button
                                className="c-button c-quiet"
                                onClick={() => setRevoke('')}
                              >
                                Cancel
                              </button>
                            </div>
                          ) : (
                            <button
                              className="c-button c-quiet"
                              onClick={() => setRevoke(k.id)}
                            >
                              Revoke
                            </button>
                          ))}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              title="A new key opens possibilities."
              action={
                <button
                  className="c-button c-primary"
                  onClick={() => {
                    setShow(true)
                    setUnlimited(true)
                    setError('')
                  }}
                >
                  Create your first key <ArrowUpRight size={16} />
                </button>
              }
            >
              Give each project its own key and a budget you control.
            </Empty>
          ))}
      </section>
    </>
  )
}

function UsagePage() {
  const [model, setModel] = useState(''),
    [key, setKey] = useState(''),
    [from, setFrom] = useState(''),
    [offset, setOffset] = useState(0)
  const query = new URLSearchParams({
    model,
    key,
    offset: String(offset),
    ...(from ? { from: new Date(from).toISOString() } : {}),
  }).toString()
  const usage = useResource<{ data: Usage[]; has_more: boolean }>(
      `/api/usage?${query}`,
    ),
    stats = useResource<Stats>(`/api/stats?${query}`),
    models = useResource<{ data: Model[] }>('/api/models'),
    keys = useResource<{ data: APIKey[] }>('/api/keys')
  return (
    <>
      <PageTitle
        title="Usage"
        description="Explore provider-reported usage and onchain payments."
      />
      <div className="c-filters">
        <label>
          Model
          <select
            value={model}
            onChange={(e) => {
              setModel(e.target.value)
              setOffset(0)
            }}
          >
            <option value="">All models</option>
            {models.data?.data.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          API key
          <select
            value={key}
            onChange={(e) => {
              setKey(e.target.value)
              setOffset(0)
            }}
          >
            <option value="">All keys + Playground</option>
            {keys.data?.data.map((k) => (
              <option key={k.id} value={k.id}>
                {k.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          From · default last 30 days
          <input
            type="date"
            value={from}
            max={new Date().toISOString().slice(0, 10)}
            min={new Date(Date.now() - 365 * 86400000)
              .toISOString()
              .slice(0, 10)}
            onChange={(e) => {
              setFrom(e.target.value)
              setOffset(0)
            }}
          />
        </label>
        <button
          className="c-button c-secondary"
          onClick={() => {
            usage.reload()
            stats.reload()
          }}
        >
          Refresh
        </button>
      </div>
      <State
        loading={false}
        error={stats.error || models.error || keys.error}
      />
      <div className="c-metrics">
        <Metric
          label="Requests"
          value={stats.data?.summary.requests.toLocaleString() ?? '—'}
        />
        <Metric
          label="Actual cost"
          value={stats.data ? `${money(stats.data.summary.spent)}` : '—'}
          sub={
            stats.data
              ? `${money(stats.data.summary.unpaid)} unpaid`
              : 'Charged after completion'
          }
        />
        <Metric
          label="Input tokens"
          value={stats.data?.summary.input_tokens.toLocaleString() ?? '—'}
        />
        <Metric
          label="Output tokens"
          value={stats.data?.summary.output_tokens.toLocaleString() ?? '—'}
        />
      </div>
      <section className="c-panel">
        <State loading={usage.loading} error={usage.error} />
        {usage.data && <UsageTable items={usage.data.data} />}
        <div className="c-pagination">
          <button
            className="c-button c-quiet"
            disabled={offset === 0 || usage.loading}
            onClick={() => setOffset(Math.max(0, offset - 50))}
          >
            Previous
          </button>
          <span>Page {offset / 50 + 1}</span>
          <button
            className="c-button c-quiet"
            disabled={!usage.data?.has_more || usage.loading}
            onClick={() => setOffset(offset + 50)}
          >
            Next
          </button>
        </div>
      </section>
    </>
  )
}
function UsageTable({ items }: { items: Usage[] }) {
  return items.length ? (
    <div className="c-table-wrap">
      <table>
        <thead>
          <tr>
            <th>Model / request</th>
            <th>Tokens · in / out</th>
            <th>Actual cost</th>
            <th>Status</th>
            <th>Time</th>
          </tr>
        </thead>
        <tbody>
          {items.map((r) => (
            <tr key={r.id}>
              <td>
                <strong>{r.model_id}</strong>
                <code title={r.id}>
                  <a href={`#receipt?request=${r.id}`}>{short(r.id)}</a> ·{' '}
                  {r.key_id ? 'API' : 'Playground'}
                </code>
              </td>
              <td>
                {r.input_tokens.toLocaleString()} /{' '}
                {r.output_tokens.toLocaleString()}
                <small>
                  {r.cache_read_tokens} cached · {r.latency_ms} ms
                </small>
              </td>
              <td>
                ${money(r.amount)}
                <small>
                  ${money(r.wallet_paid_amount)} wallet paid
                  <br />${money(r.unpaid_amount)} outstanding
                  {Number(r.waived_amount ?? 0) > 0 && (
                    <>
                      <br />${money(r.waived_amount!)} waived
                    </>
                  )}
                </small>
              </td>
              <td>
                <Badge
                  tone={
                    r.state === 'completed'
                      ? 'lime'
                      : r.state === 'usage_unknown'
                        ? 'amber'
                        : ''
                  }
                >
                  {r.state.replaceAll('_', ' ')}
                </Badge>
                {r.error_code && <small>{r.error_code}</small>}
              </td>
              <td>{date(r.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  ) : (
    <Empty
      title="No requests here yet."
      action={<Go to="playground">Try a model</Go>}
    >
      Once you make a request, its usage and cost appear here.
    </Empty>
  )
}

function Models() {
  const models = useResource<{ data: Model[] }>('/api/models'),
    [search, setSearch] = useState('')
  const items = models.data?.data.filter((m) =>
    `${m.name} ${m.vendor} ${m.id}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  )
  return (
    <>
      <PageTitle
        title="Model library"
        description="Choose your intelligence. Review context tiers, token rates and tool fees in USDC, before account discounts."
      />
      <div className="c-filters">
        <label className="c-search">
          Find a model
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search models or providers…"
          />
        </label>
      </div>
      <State loading={models.loading} error={models.error} />
      <div className="c-models">
        {items?.map((m) => {
          const company = modelBrand(m.vendor)
          return (
            <article className="c-panel c-model" key={m.id}>
              <div className="c-model-top">
                <span className="c-model-glyph" data-brand={company?.id}>
                  {company ? (
                    <img
                      src={company.src}
                      alt={`${company.name} logo`}
                      width={28}
                      height={28}
                    />
                  ) : (
                    <Box size={24} aria-label={m.vendor || 'Model provider'} />
                  )}
                </span>
                <Badge tone="lime">Available</Badge>
              </div>
              <small>{m.vendor}</small>
              <h2>{m.name}</h2>
              <CopyValue value={m.id} label="Copy model ID" />
              <PublicModelPricing model={m} />
            </article>
          )
        })}
      </div>
      {items?.length === 0 && (
        <section className="c-panel">
          <Empty
            title={search ? 'No matching models.' : 'No models available'}
          >
            {search
              ? 'Try another model or provider name.'
              : 'Contact the platform administrator to enable model access.'}
          </Empty>
        </section>
      )}
    </>
  )
}

function Quickstart() {
  const base = `${location.origin}/v1`
  const claudeCode = `unset ANTHROPIC_API_KEY
export ANTHROPIC_BASE_URL="${location.origin}"
export ANTHROPIC_AUTH_TOKEN="sk-YOUR_TOKENSWOOP_KEY"
export ANTHROPIC_MODEL="YOUR_MODEL_ID"
export ANTHROPIC_DEFAULT_OPUS_MODEL="$ANTHROPIC_MODEL"
export ANTHROPIC_DEFAULT_SONNET_MODEL="$ANTHROPIC_MODEL"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="$ANTHROPIC_MODEL"
export CLAUDE_CODE_SUBAGENT_MODEL="$ANTHROPIC_MODEL"

claude --model "$ANTHROPIC_MODEL"`
  const codexConfig = `model = "YOUR_MODEL_ID"
model_provider = "tokenswoop"
web_search = "disabled"

[model_providers.tokenswoop]
name = "TokenSwoop"
base_url = "${base}"
env_key = "TOKENSWOOP_API_KEY"
wire_api = "responses"
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0`
  const codexStart = `export TOKENSWOOP_API_KEY="sk-YOUR_TOKENSWOOP_KEY"
codex`
  const code = `import os\nfrom openai import OpenAI\n\nclient = OpenAI(\n    api_key=os.environ["TOKENSWOOP_API_KEY"],\n    base_url="${base}",\n    max_retries=0,\n)\n\nstream = client.chat.completions.create(\n    model="YOUR_MODEL_ID",\n    messages=[{"role": "user", "content": "Hello, orbit."}],\n    max_completion_tokens=256,\n    stream=True,\n    stream_options={"include_usage": True},\n)\n\nfor event in stream:\n    if event.choices:\n        print(event.choices[0].delta.content or "", end="")`
  return (
    <>
      <PageTitle
        title="Make your first call"
        description="Connect your app, Claude Code or Codex with a TokenSwoop API key."
      />
      <div className="c-overview-grid">
        <section className="c-panel c-docs">
          <div className="c-panel-title">
            <h2>
              <Terminal size={19} /> Python quickstart
            </h2>
            <CopyButton value={code} label="Copy Python example" />
          </div>
          <pre>
            <code>{code}</code>
          </pre>
        </section>
        <section className="c-panel c-docs">
          <span className="c-eyebrow">THREE THINGS TO KNOW</span>
          <h2>Your connection details</h2>
          <label>API base URL</label>
          <CopyValue
            className="c-inline-code"
            value={base}
            label="Copy API base URL"
          />
          <ol>
            <li>
              <a href="#billing">Set up wallet payments</a> with a funded wallet
              and a spending allowance.
            </li>
            <li>
              <a href="#keys">Create an API key</a> and set its spending limit.
              Use it in your app or coding client.
            </li>
            <li>
              <a href="#models">Pick an available model</a> and replace
              YOUR_MODEL_ID in the example.
            </li>
          </ol>
          <Notice>
            Function tools run in your application. Hosted web/file search
            requires explicit model pricing and a compatible channel.
          </Notice>
        </section>
      </div>
      <div className="c-quickstart-clients">
        <section className="c-panel c-docs c-client-guide">
          <div className="c-panel-title">
            <h2>
              <Terminal size={19} /> Claude Code
            </h2>
            <CopyButton value={claudeCode} label="Copy Claude Code setup" />
          </div>
          <p>
            With Claude Code installed, replace the key and YOUR_MODEL_ID, then
            run this in a Bash or Zsh terminal. Use the site address without
            /v1; Claude Code adds /v1/messages.
          </p>
          <pre>
            <code>{claudeCode}</code>
          </pre>
          <p className="c-muted">
            The example maps common model aliases to your chosen model. Start
            Claude Code from this terminal and use /status to check the base URL
            and active model. These environment variables last for this terminal
            session.
          </p>
          <a
            href="https://code.claude.com/docs/en/llm-gateway-connect"
            target="_blank"
            rel="noreferrer"
          >
            Claude Code configuration docs ↗
          </a>
        </section>
        <section className="c-panel c-docs c-client-guide">
          <h2>
            <Terminal size={19} /> Codex
          </h2>
          <p>
            With Codex CLI installed, merge these settings into your user-level
            ~/.codex/config.toml and replace YOUR_MODEL_ID. Keep the first three
            settings above any [table] sections.
          </p>
          <div className="c-docs-snippet-title">
            <h3>~/.codex/config.toml</h3>
            <CopyButton value={codexConfig} label="Copy Codex configuration" />
          </div>
          <pre>
            <code>{codexConfig}</code>
          </pre>
          <div className="c-docs-snippet-title">
            <h3>Start in Bash or Zsh</h3>
            <CopyButton value={codexStart} label="Copy Codex launch command" />
          </div>
          <pre>
            <code>{codexStart}</code>
          </pre>
          <p className="c-muted">
            This uses /v1/responses with HTTP streaming. Hosted web search is
            disabled for this setup. TokenSwoop handles upstream retries; client
            retries are disabled to avoid submitting extra requests.
          </p>
          <a
            href="https://developers.openai.com/codex/config-advanced/"
            target="_blank"
            rel="noreferrer"
          >
            Codex configuration docs ↗
          </a>
        </section>
      </div>
      <section className="c-panel c-docs">
        <h2>When a request needs attention</h2>
        <div className="c-table-wrap">
          <table>
            <thead>
              <tr>
                <th>Status</th>
                <th>Meaning</th>
                <th>Next step</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>401 / 403</td>
                <td>Key, account or model permission</td>
                <td>Check key status and selected model.</td>
              </tr>
              <tr>
                <td>402</td>
                <td>
                  Settlement failed due to insufficient balance, or key budget
                  reached
                </td>
                <td>
                  Add funds and settle the failed bill, or increase the key
                  budget.
                </td>
              </tr>
              <tr>
                <td>429</td>
                <td>Account rate or concurrency limit</td>
                <td>Wait for active requests, then retry with backoff.</td>
              </tr>
              <tr>
                <td>502 / 503</td>
                <td>Upstream unavailable or result uncertain</td>
                <td>Save X-Request-ID and inspect Usage before retrying.</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p className="c-muted">
          Each accepted request captures its price and tier. A new client
          submission is a new billable request; automatic client retries can
          produce additional calls.
        </p>
      </section>
    </>
  )
}
