import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Download, Plus, RefreshCw, X } from 'lucide-react'
import { api, money, useResource } from './api'
import type { Channel, Model, ChannelOptions } from './api'
import { ChannelCompatibility } from './ChannelCompatibility'
import { PricingEditor } from './Pricing'
import { Badge, Empty, Notice, State } from './ui'

type DiscoveredModel = {
  id: string
  name: string
  vendor: string
  max_input_tokens?: number
  max_output_tokens?: number
}
type Mapping = { key: string; publicID: string; upstreamID: string }
type ChannelDraft = {
  options: ChannelOptions
  id?: string
  revision: number
  name: string
  protocol: string
  base_url: string
  secret: string
  priority: number
  enabled: boolean
}
const protocols: Record<string, { label: string; url: string }> = {
  openai: { label: 'OpenAI Chat compatible', url: 'https://api.openai.com/v1' },
  openai_responses: {
    label: 'OpenAI Responses',
    url: 'https://api.openai.com/v1',
  },
  claude: { label: 'Anthropic Messages', url: 'https://api.anthropic.com/v1' },
  gemini: {
    label: 'Google Gemini',
    url: 'https://generativelanguage.googleapis.com/v1beta',
  },
}
const idPattern = /^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,127}$/
const modelLink = (id: string) =>
  `#admin?section=models&model=${encodeURIComponent(id)}`
const channelLink = (id: string) =>
  `#admin?section=channels&channel=${encodeURIComponent(id)}`
const mapping = (upstreamID: string, publicID = upstreamID): Mapping => ({
  key: crypto.randomUUID(),
  publicID,
  upstreamID,
})

function useOperation() {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [message, setMessage] = useState('')
  async function run(work: () => Promise<string>) {
    setBusy(true)
    setError('')
    setMessage('')
    try {
      setMessage(await work())
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return {
    busy,
    error,
    message,
    run,
    clear: () => {
      setError('')
      setMessage('')
    },
  }
}
function Feedback({ action }: { action: ReturnType<typeof useOperation> }) {
  return (
    <>
      {action.error && <Notice error>{action.error}</Notice>}
      {action.message && <Notice>{action.message}</Notice>}
    </>
  )
}

export function ChannelManager({ initialID = '' }: { initialID?: string }) {
  const channels = useResource<{ data: Channel[] }>(
    '/api/admin/channels',
    true,
    true,
  )
  const models = useResource<{ data: Model[] }>('/api/admin/models', true, true)
  const action = useOperation(),
    discovery = useOperation()
  const [draft, setDraft] = useState<ChannelDraft | null>(null)
  const [rows, setRows] = useState<Mapping[]>([]),
    [catalog, setCatalog] = useState<DiscoveredModel[] | null>(null)
  const [search, setSearch] = useState(''),
    [modelSearch, setModelSearch] = useState(''),
    [manual, setManual] = useState('')
  const [created, setCreated] = useState<string[]>([]),
    [testChannel, setTestChannel] = useState<Channel | null>(null),
    [testModel, setTestModel] = useState('')
  const opened = useRef(false)
  const formRef = useRef<HTMLFormElement>(null)
  const formOpen = draft !== null
  useEffect(() => {
    if (formOpen) formRef.current?.scrollIntoView({ block: 'start' })
  }, [formOpen, draft?.id])
  const busy = action.busy || discovery.busy
  const known = new Map((models.data?.data ?? []).map((m) => [m.id, m]))
  function start(ch?: Channel) {
    setDraft({
      id: ch?.id,
      revision: ch?.revision ?? 0,
      options: ch?.options ?? {},
      name: ch?.name ?? '',
      protocol: ch?.protocol ?? 'openai',
      base_url: ch?.base_url ?? '',
      secret: '',
      priority: ch?.priority ?? 100,
      enabled: ch?.enabled ?? true,
    })
    setRows(
      Object.entries(ch?.model_map ?? {}).map(([publicID, upstreamID]) =>
        mapping(upstreamID, publicID),
      ),
    )
    setCatalog(null)
    setModelSearch('')
    setManual('')
    setCreated([])
    setTestChannel(null)
    action.clear()
    discovery.clear()
  }
  useEffect(() => {
    if (!opened.current && initialID && channels.data) {
      opened.current = true
      const ch = channels.data.data.find((c) => c.id === initialID)
      if (ch) start(ch)
    }
  }, [channels.data, initialID])
  const available = (catalog ?? []).filter((m) =>
    `${m.id} ${m.name} ${m.vendor}`
      .toLowerCase()
      .includes(modelSearch.toLowerCase()),
  )
  const picked = new Set(rows.map((r) => r.upstreamID))
  const pendingCount = rows.filter((r) => !known.has(r.publicID)).length
  function select(list: DiscoveredModel[]) {
    const additions = list.filter((m) => !picked.has(m.id))
    if (rows.length + additions.length > 500) {
      void action.run(async () => {
        throw new Error(
          'A channel supports up to 500 mappings. Narrow the model search before selecting all.',
        )
      })
      return
    }
    setRows([...rows, ...additions.map((m) => mapping(m.id))])
  }
  function addManual() {
    const id = manual.trim()
    if (!idPattern.test(id) || rows.length >= 500) {
      void action.run(async () => {
        throw new Error(
          'Enter a valid model ID (1–128 characters). A channel supports up to 500 mappings.',
        )
      })
      return
    }
    // Manual entries may reuse an upstream ID under a different public alias.
    setRows([...rows, mapping(id)])
    setManual('')
  }
  const fetchModels = () =>
    void discovery.run(async () => {
      if (!draft) return ''
      if (!draft.base_url.trim() || (!draft.id && !draft.secret.trim()))
        throw new Error('Enter the API base URL and upstream API key first.')
      const result = await api<{ data: DiscoveredModel[]; skipped: number }>(
        `/api/admin/channels${draft.id ? `/${draft.id}` : ''}/fetch-models`,
        {
          method: 'POST',
          body: JSON.stringify({
            protocol: draft.protocol,
            base_url: draft.base_url,
            secret: draft.secret,
            options: draft.options,
          }),
        },
      )
      setCatalog(result.data)
      return `Fetched ${result.data.length} models.${result.skipped ? ` Skipped ${result.skipped} unsupported entries.` : ''} Existing selections are preserved.`
    })
  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    void action.run(async () => {
      if (!draft) return ''
      const map: Record<string, string> = {},
        defaults: Record<string, DiscoveredModel> = {}
      for (const row of rows) {
        const id = row.publicID.trim(),
          upstream = row.upstreamID.trim()
        if (!idPattern.test(id) || !idPattern.test(upstream))
          throw new Error(
            'Check each public and upstream model ID: use 1–128 letters, digits, dots, slashes, colons, underscores or hyphens.',
          )
        if (Object.hasOwn(map, id))
          throw new Error(
            `Public model ID “${id}” is mapped more than once. Give each mapping a unique public ID.`,
          )
        Object.defineProperty(map, id, { value: upstream, enumerable: true })
        const info = catalog?.find((m) => m.id === upstream)
        if (info)
          Object.defineProperty(defaults, id, { value: info, enumerable: true })
      }
      const result = await api<{ created_models: string[] }>(
        `/api/admin/channels${draft.id ? `/${draft.id}` : ''}`,
        {
          method: draft.id ? 'PUT' : 'POST',
          body: JSON.stringify({
            ...draft,
            model_map: map,
            enabled: rows.length > 0 && draft.enabled,
            create_missing_models: true,
            model_defaults: defaults,
          }),
        },
      )
      setCreated(result.created_models)
      setDraft(null)
      setRows([])
      setCatalog(null)
      channels.reload()
      models.reload()
      return result.created_models.length
        ? `Channel saved. ${result.created_models.length} new model drafts are ready for pricing and capability configuration.`
        : 'Channel saved. Existing model pricing is preserved.'
    })
  }
  const visibleChannels = (channels.data?.data ?? []).filter((ch) =>
    `${ch.name} ${ch.protocol} ${ch.base_url} ${Object.keys(ch.model_map).join(' ')}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  )
  return (
    <>
      <div className="c-section-actions">
        <p className="c-muted">
          Connect an upstream, fetch its models, then choose what to offer.
          Higher priorities are tried first.
        </p>
        <button
          className="c-button c-primary"
          disabled={busy}
          onClick={() => start()}
        >
          <Plus size={16} /> Add channel
        </button>
      </div>
      <Feedback action={action} />
      {created.length > 0 && (
        <section className="c-panel c-import-next">
          <div>
            <Badge tone="amber">NEXT · CONFIGURE MODELS</Badge>
            <h3>Set prices before publishing.</h3>
            <p className="c-muted">
              Imported models stay disabled until you enter prices and enable
              them. Existing models keep their settings.
            </p>
          </div>
          <div className="c-model-links">
            {created.slice(0, 12).map((id) => (
              <a
                className="c-button c-secondary c-small"
                href={modelLink(id)}
                key={id}
              >
                {id}
                <ArrowRight size={13} />
              </a>
            ))}
          </div>
          <a className="c-text-link" href="#admin?section=models">
            Open all models <ArrowRight size={14} />
          </a>
        </section>
      )}
      {draft && (
        <form
          ref={formRef}
          className="c-panel c-form c-channel-form"
          onSubmit={save}
        >
          <div className="c-panel-title">
            <div>
              <span className="c-eyebrow">
                {draft.id ? 'CHANNEL SETTINGS' : 'NEW CONNECTION'}
              </span>
              <h2>
                {draft.id ? 'Edit upstream channel' : 'Connect an upstream'}
              </h2>
            </div>
            <button
              type="button"
              className="c-button c-quiet"
              disabled={busy}
              onClick={() => {
                setDraft(null)
                setCatalog(null)
                discovery.clear()
              }}
            >
              Close
            </button>
          </div>
          <fieldset disabled={busy} className="c-connection-fields">
            <legend>01 · Connection</legend>
            <div className="c-form-grid">
              <label>
                Channel name
                <input
                  required
                  maxLength={100}
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder="Primary provider"
                />
              </label>
              <label>
                Upstream protocol
                <select
                  value={draft.protocol}
                  onChange={(e) => {
                    const protocol = e.target.value
                    const replaceURL =
                      !draft.base_url ||
                      Object.values(protocols).some(
                        (p) => p.url === draft.base_url,
                      )
                    setDraft({
                      ...draft,
                      protocol,
                      base_url: replaceURL
                        ? protocols[protocol].url
                        : draft.base_url,
                    })
                    setCatalog(null)
                    discovery.clear()
                  }}
                >
                  {Object.entries(protocols).map(([id, p]) => (
                    <option key={id} value={id}>
                      {p.label}
                    </option>
                  ))}
                </select>
              </label>
              <label className="c-wide-field">
                API base URL · include /v1 or /v1beta
                <input
                  type="url"
                  required
                  value={draft.base_url}
                  onChange={(e) => {
                    setDraft({ ...draft, base_url: e.target.value })
                    setCatalog(null)
                    discovery.clear()
                  }}
                  placeholder={protocols[draft.protocol]?.url}
                />
              </label>
              <label>
                {draft.id
                  ? 'API key · leave blank to keep current'
                  : 'Upstream API key'}
                <input
                  type="password"
                  autoComplete="new-password"
                  required={!draft.id}
                  maxLength={8192}
                  value={draft.secret}
                  onChange={(e) => {
                    setDraft({ ...draft, secret: e.target.value })
                    setCatalog(null)
                    discovery.clear()
                  }}
                />
              </label>
              <label>
                Priority · highest first
                <input
                  type="number"
                  required
                  min={0}
                  max={10000}
                  value={draft.priority}
                  onChange={(e) =>
                    setDraft({ ...draft, priority: Number(e.target.value) })
                  }
                />
              </label>
            </div>
            <p className="c-muted">
              Use the final API endpoint of your provider or proxy. Changing the
              host or port requires entering an API key again.
            </p>
          </fieldset>
          <fieldset disabled={busy}>
            <ChannelCompatibility
              key={draft.id || 'new'}
              value={draft.options}
              change={(options) => setDraft({ ...draft, options })}
            />
          </fieldset>
          <fieldset disabled={busy}>
            <legend>02 · Discover & select</legend>
            <div className="c-discovery-heading">
              <div>
                <h3>Choose upstream models</h3>
                <p className="c-muted">
                  Fetching reads the model list. It does not generate content or
                  create a usage charge.
                </p>
              </div>
              <button
                type="button"
                className="c-button c-secondary"
                onClick={fetchModels}
              >
                <Download size={15} />
                {discovery.busy ? 'Fetching…' : 'Fetch models'}
              </button>
            </div>
            <Feedback action={discovery} />
            {catalog !== null && (
              <div className="c-discovery-box">
                <div className="c-discovery-tools">
                  <label>
                    Search fetched models
                    <input
                      value={modelSearch}
                      onChange={(e) => setModelSearch(e.target.value)}
                      placeholder="Model ID, name or provider"
                    />
                  </label>
                  <button
                    type="button"
                    className="c-button c-quiet"
                    onClick={() => select(available)}
                  >
                    Select matching ({available.length})
                  </button>
                  <button
                    type="button"
                    className="c-button c-quiet"
                    onClick={() => {
                      const ids = new Set(available.map((m) => m.id))
                      setRows(rows.filter((r) => !ids.has(r.upstreamID)))
                    }}
                  >
                    Deselect matching
                  </button>
                </div>
                <div className="c-discovery-list">
                  {available.slice(0, 150).map((m) => (
                    <label key={m.id} className="c-discovered-model">
                      <input
                        type="checkbox"
                        checked={picked.has(m.id)}
                        onChange={(e) =>
                          e.target.checked
                            ? select([m])
                            : setRows(rows.filter((r) => r.upstreamID !== m.id))
                        }
                      />
                      <span>
                        <strong>{m.name}</strong>
                        <code>{m.id}</code>
                      </span>
                      <small>{m.vendor || 'Compatible'}</small>
                    </label>
                  ))}
                  {!available.length && (
                    <p className="c-muted">
                      {catalog.length
                        ? 'No models match this search.'
                        : 'The upstream returned an empty list. You can still add model IDs manually.'}
                    </p>
                  )}
                </div>
                {available.length > 150 && (
                  <p className="c-muted">
                    Showing 150 of {available.length}. Narrow the search to see
                    more; bulk selection includes all matching entries.
                  </p>
                )}
              </div>
            )}
            <div className="c-manual-model">
              <label>
                Add a model ID manually
                <input
                  value={manual}
                  onChange={(e) => setManual(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      addManual()
                    }
                  }}
                  maxLength={128}
                  placeholder="provider-model-id"
                />
              </label>
              <button
                type="button"
                className="c-button c-secondary"
                onClick={addManual}
                disabled={!manual.trim()}
              >
                <Plus size={14} />
                Add
              </button>
            </div>
            <p className="c-muted">
              Select text generation models supported by this gateway. Provider
              lists can also contain image, audio or embedding models.
            </p>
          </fieldset>
          <fieldset disabled={busy}>
            <legend>03 · Public model mapping · {rows.length} / 500</legend>
            <p className="c-muted">
              Clients send the public ID; this channel forwards the upstream ID.
              Reuse a public ID across channels for fallback routing.
            </p>
            <datalist id="existing-public-models">
              {(models.data?.data ?? []).map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </datalist>
            {rows.length ? (
              <div className="c-mapping-list">
                {rows.map((row) => (
                  <div className="c-mapping-row" key={row.key}>
                    <label>
                      Public model ID
                      <input
                        required
                        maxLength={128}
                        list="existing-public-models"
                        value={row.publicID}
                        onChange={(e) =>
                          setRows(
                            rows.map((r) =>
                              r.key === row.key
                                ? { ...r, publicID: e.target.value }
                                : r,
                            ),
                          )
                        }
                      />
                    </label>
                    <ArrowRight size={16} className="c-mapping-arrow" />
                    <label>
                      Upstream model ID
                      <input
                        required
                        maxLength={128}
                        value={row.upstreamID}
                        onChange={(e) =>
                          setRows(
                            rows.map((r) =>
                              r.key === row.key
                                ? { ...r, upstreamID: e.target.value }
                                : r,
                            ),
                          )
                        }
                      />
                    </label>
                    <Badge
                      tone={
                        known.get(row.publicID)?.pricing_configured
                          ? ''
                          : 'amber'
                      }
                    >
                      {known.has(row.publicID)
                        ? known.get(row.publicID)?.pricing_configured
                          ? 'Existing model'
                          : 'Draft'
                        : 'New draft'}
                    </Badge>
                    <button
                      type="button"
                      className="c-button c-quiet"
                      aria-label={`Remove mapping ${row.publicID}`}
                      onClick={() =>
                        setRows(rows.filter((r) => r.key !== row.key))
                      }
                    >
                      <X size={15} />
                    </button>
                  </div>
                ))}
              </div>
            ) : (
              <p className="c-mapping-empty">
                Select models above or add an ID manually. You can also save
                this connection now as a disabled channel.
              </p>
            )}
            {pendingCount > 0 && (
              <p className="c-muted">
                Saving will create {pendingCount} disabled model draft
                {pendingCount === 1 ? '' : 's'}. Prices and capabilities are
                configured in Models.
              </p>
            )}
          </fieldset>
          <div className="c-channel-save">
            <label className="c-check">
              <input
                type="checkbox"
                checked={draft.enabled && rows.length > 0}
                disabled={!rows.length || busy}
                onChange={(e) =>
                  setDraft({ ...draft, enabled: e.target.checked })
                }
              />
              Enable channel for configured models
            </label>
            <button className="c-button c-primary" disabled={busy}>
              {action.busy
                ? 'Saving…'
                : rows.length
                  ? 'Save channel & mappings'
                  : 'Save disabled connection'}
            </button>
          </div>
        </form>
      )}
      <div className="c-filters">
        <label className="c-search">
          Search channels
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Name, URL, protocol or model"
          />
        </label>
        <button
          className="c-button c-secondary"
          disabled={busy || channels.loading}
          onClick={() => {
            channels.reload()
            models.reload()
          }}
        >
          <RefreshCw size={14} />
          Refresh
        </button>
        <Badge>{channels.data?.data.length ?? 0} channels</Badge>
      </div>
      <State
        loading={channels.loading}
        error={channels.error || models.error}
      />
      {channels.data && (
        <section className="c-panel">
          {visibleChannels.length ? (
            <div className="c-table-wrap c-catalog-table">
              <table>
                <thead>
                  <tr>
                    <th>Channel / upstream</th>
                    <th>Models</th>
                    <th>Priority</th>
                    <th>Status</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {visibleChannels.map((ch) => {
                    const ids = Object.keys(ch.model_map),
                      live = ids.filter(
                        (id) =>
                          known.get(id)?.enabled &&
                          known.get(id)?.pricing_configured,
                      )
                    const cooling =
                      !!ch.disabled_until &&
                      new Date(ch.disabled_until) > new Date()
                    const state = !ch.enabled
                      ? 'Disabled'
                      : !ids.length
                        ? 'No models'
                        : !live.length
                          ? 'Configure models'
                          : cooling
                            ? 'Cooling down'
                            : ch.failure_count
                              ? `${ch.failure_count} failures`
                              : 'Enabled'
                    return (
                      <tr key={ch.id}>
                        <td>
                          <strong>{ch.name}</strong>
                          <small>
                            {protocols[ch.protocol]?.label ?? ch.protocol}
                          </small>
                          <span className="c-upstream-url">{ch.base_url}</span>
                        </td>
                        <td>
                          <span>
                            {live.length} enabled / {ids.length} mapped
                          </span>
                          <div className="c-channel-models">
                            {ids.slice(0, 3).map((id) => (
                              <a href={modelLink(id)} key={id}>
                                {id}
                              </a>
                            ))}
                            {ids.length > 3 && (
                              <button
                                className="c-button c-quiet c-small"
                                disabled={busy}
                                onClick={() => start(ch)}
                              >
                                +{ids.length - 3} more
                              </button>
                            )}
                          </div>
                        </td>
                        <td>{ch.priority}</td>
                        <td>
                          <Badge tone={state === 'Enabled' ? 'lime' : 'amber'}>
                            {state}
                          </Badge>
                          {ch.last_error && <small>{ch.last_error}</small>}
                        </td>
                        <td>
                          <div className="c-row-actions c-wrap">
                            <button
                              className="c-button c-quiet"
                              disabled={busy}
                              onClick={() => start(ch)}
                            >
                              Edit / models
                            </button>
                            <button
                              className="c-button c-quiet"
                              disabled={busy || !live.length}
                              onClick={() => {
                                setTestChannel(ch)
                                setTestModel(live[0])
                                action.clear()
                              }}
                            >
                              Test
                            </button>
                            <button
                              className="c-button c-quiet"
                              disabled={busy || (!ch.enabled && !ids.length)}
                              onClick={() =>
                                void action.run(async () => {
                                  await api(`/api/admin/channels/${ch.id}`, {
                                    method: 'PUT',
                                    body: JSON.stringify({
                                      ...ch,
                                      enabled: !ch.enabled,
                                    }),
                                  })
                                  channels.reload()
                                  if (draft?.id === ch.id) setDraft(null)
                                  return ch.enabled
                                    ? 'Channel disabled.'
                                    : 'Channel enabled.'
                                })
                              }
                            >
                              {ch.enabled ? 'Disable' : 'Enable'}
                            </button>
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              title={
                search ? 'No matching channels.' : 'Your supply starts here.'
              }
            >
              {search
                ? 'Try another name, URL or model ID.'
                : 'Add a channel with its API URL and key, then fetch the available models.'}
            </Empty>
          )}
        </section>
      )}
      {testChannel && (
        <section className="c-panel c-form">
          <div className="c-panel-title">
            <h2>Test · {testChannel.name}</h2>
            <button
              className="c-button c-quiet"
              disabled={busy}
              onClick={() => setTestChannel(null)}
            >
              Close
            </button>
          </div>
          <p className="c-muted">
            Sends a real generation request. Actual usage is charged to your
            wallet after completion through onchain settlement.
          </p>
          <label>
            Configured model
            <select
              value={testModel}
              disabled={busy}
              onChange={(e) => setTestModel(e.target.value)}
            >
              {Object.keys(testChannel.model_map)
                .filter(
                  (id) =>
                    known.get(id)?.enabled && known.get(id)?.pricing_configured,
                )
                .map((id) => (
                  <option key={id} value={id}>
                    {id} → {testChannel.model_map[id]}
                  </option>
                ))}
            </select>
          </label>
          <button
            className="c-button c-secondary"
            disabled={busy || !testModel}
            onClick={() =>
              void action.run(async () => {
                await api(`/api/admin/channels/${testChannel.id}/test`, {
                  method: 'POST',
                  body: JSON.stringify({
                    model: testModel,
                    messages: [{ role: 'user', content: 'Reply with OK.' }],
                    max_completion_tokens: 32,
                  }),
                })
                channels.reload()
                setTestChannel(null)
                return 'Generation test succeeded. Confirmed usage was recorded and billed after completion.'
              })
            }
          >
            {action.busy ? 'Testing…' : 'Run metered test'}
          </button>
        </section>
      )}
    </>
  )
}

const priceFields = [
  'input_price',
  'output_price',
  'cache_read_price',
  'cache_write_price',
  'cache_write_1h_price',
] as const
const priceLabels = [
  'Input',
  'Output',
  'Cache read',
  'Cache write · 5 minutes',
  'Cache write · 1 hour',
]
export function ModelManager({ initialID = '' }: { initialID?: string }) {
  const models = useResource<{ data: Model[] }>(
      '/api/admin/models',
      true,
      true,
    ),
    channels = useResource<{ data: Channel[] }>(
      '/api/admin/channels',
      true,
      true,
    )
  const action = useOperation(),
    [editing, setEditing] = useState<Model | null>(null),
    [show, setShow] = useState(false)
  const [search, setSearch] = useState(''),
    [status, setStatus] = useState('all'),
    [channel, setChannel] = useState('all')
  const opened = useRef(false)
  const formRef = useRef<HTMLFormElement>(null)
  useEffect(() => {
    if (show) formRef.current?.scrollIntoView({ block: 'start' })
  }, [show, editing?.id])
  useEffect(() => {
    if (!opened.current && initialID && models.data) {
      opened.current = true
      const m = models.data.data.find((m) => m.id === initialID)
      if (m) {
        setEditing(m)
        setShow(true)
      }
    }
  }, [models.data, initialID])
  const selectedChannel = channels.data?.data.find((c) => c.id === channel)
  const visible = (models.data?.data ?? []).filter(
    (m) =>
      `${m.id} ${m.name} ${m.vendor}`
        .toLowerCase()
        .includes(search.toLowerCase()) &&
      (status === 'all' ||
        (status === 'draft'
          ? !m.pricing_configured
          : status === 'enabled'
            ? m.enabled
            : m.pricing_configured && !m.enabled)) &&
      (channel === 'all' ||
        (selectedChannel && Object.hasOwn(selectedChannel.model_map, m.id))),
  )
  const drafts =
    models.data?.data.filter((m) => !m.pricing_configured).length ?? 0
  function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    void action.run(async () => {
      await api('/api/admin/models', {
        method: 'PUT',
        body: JSON.stringify({
          id: f.get('id'),
          name: f.get('name'),
          vendor: f.get('vendor'),
          enabled: f.has('enabled'),
          tools: f.has('tools'),
          structured_output: f.has('structured_output'),
          ...Object.fromEntries(priceFields.map((k) => [k, f.get(k)])),
          pricing: JSON.parse(String(f.get('pricing') || '{}')),
        }),
      })
      setShow(false)
      models.reload()
      return 'Model configuration saved. Existing requests keep their original price snapshot.'
    })
  }
  return (
    <>
      <div className="c-section-actions">
        <p className="c-muted">
          Set public model IDs, capabilities and prices.{' '}
          {drafts > 0
            ? `${drafts} imported model${drafts === 1 ? ' needs' : 's need'} configuration.`
            : 'Publish capabilities validated against your upstream.'}
        </p>
        <div className="c-row-actions c-wrap">
          <a className="c-button c-secondary" href="#admin?section=channels">
            <Download size={15} />
            Import from channel
          </a>
          <button
            className="c-button c-primary"
            disabled={action.busy}
            onClick={() => {
              setEditing(null)
              setShow(true)
              action.clear()
            }}
          >
            <Plus size={15} />
            Add model
          </button>
        </div>
      </div>
      <Feedback action={action} />
      {show && (
        <form
          ref={formRef}
          className="c-panel c-form"
          key={editing?.id ?? 'new'}
          onSubmit={save}
        >
          <div className="c-panel-title">
            <div>
              <span className="c-eyebrow">MODEL CONFIGURATION</span>
              <h2>
                {editing
                  ? editing.pricing_configured
                    ? 'Edit model & pricing'
                    : 'Configure imported model'
                  : 'Add a model'}
              </h2>
            </div>
            <button
              type="button"
              className="c-button c-quiet"
              disabled={action.busy}
              onClick={() => setShow(false)}
            >
              Close
            </button>
          </div>
          {editing && !editing.pricing_configured && (
            <Notice>
              This draft is not available to customers. Confirm capabilities,
              enter all prices, then enable it.
            </Notice>
          )}
          <fieldset disabled={action.busy}>
            <legend>Identity</legend>
            <div className="c-form-grid">
              <label>
                Public model ID
                <input
                  name="id"
                  required
                  readOnly={!!editing}
                  maxLength={128}
                  defaultValue={editing?.id}
                />
              </label>
              <label>
                Display name
                <input
                  name="name"
                  required
                  maxLength={100}
                  defaultValue={editing?.name}
                />
              </label>
              <label>
                Provider name
                <input
                  name="vendor"
                  required
                  maxLength={80}
                  defaultValue={
                    editing?.vendor === 'Unconfigured' ? '' : editing?.vendor
                  }
                />
              </label>
            </div>
          </fieldset>
          <fieldset disabled={action.busy}>
            <legend>Capabilities & availability</legend>
            <div className="c-checks">
              <label>
                <input
                  name="enabled"
                  type="checkbox"
                  defaultChecked={
                    editing?.pricing_configured ? editing.enabled : true
                  }
                />
                Enable model
              </label>
              <label>
                <input
                  name="tools"
                  type="checkbox"
                  defaultChecked={editing?.tools}
                />
                Function tools
              </label>
              <label>
                <input
                  name="structured_output"
                  type="checkbox"
                  defaultChecked={editing?.structured_output}
                />
                Structured output
              </label>
            </div>
            <p className="c-muted">
              A model also needs an enabled channel mapping to serve requests.
              Discovery does not verify tool or structured output support.
            </p>
          </fieldset>
          <fieldset disabled={action.busy}>
            <legend>USDC per million tokens</legend>
            <div className="c-form-grid">
              {priceFields.map((k, i) => (
                <label key={k}>
                  {priceLabels[i]}
                  <input
                    name={k}
                    required
                    inputMode="decimal"
                    placeholder="Enter price, or 0"
                    defaultValue={editing?.pricing_configured ? editing[k] : ''}
                  />
                </label>
              ))}
            </div>
            <p className="c-muted">
              Enter 0 explicitly for a free token category. Saving creates a
              price version; requests are charged only after confirmed usage.
            </p>
          </fieldset>
          <PricingEditor key={editing?.id ?? 'new'} model={editing} />
          {editing && (
            <div className="c-model-links">
              <span className="c-muted">Mapped channels:</span>
              {channels.data?.data
                .filter((ch) => Object.hasOwn(ch.model_map, editing.id))
                .map((ch) => (
                  <a
                    className="c-button c-secondary c-small"
                    key={ch.id}
                    href={channelLink(ch.id)}
                  >
                    {ch.name}
                    <ArrowRight size={13} />
                  </a>
                ))}
            </div>
          )}
          <button className="c-button c-primary" disabled={action.busy}>
            {action.busy ? 'Saving…' : 'Save model & prices'}
          </button>
        </form>
      )}
      <div className="c-filters">
        <label className="c-search">
          Search models
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Public ID, name or provider"
          />
        </label>
        <label>
          Status
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="all">All models</option>
            <option value="draft">Needs configuration ({drafts})</option>
            <option value="enabled">Enabled</option>
            <option value="disabled">Disabled</option>
          </select>
        </label>
        <label>
          Channel
          <select value={channel} onChange={(e) => setChannel(e.target.value)}>
            <option value="all">All channels</option>
            {channels.data?.data.map((ch) => (
              <option key={ch.id} value={ch.id}>
                {ch.name}
              </option>
            ))}
          </select>
        </label>
        <button
          className="c-button c-secondary"
          disabled={action.busy || models.loading}
          onClick={() => {
            models.reload()
            channels.reload()
          }}
        >
          <RefreshCw size={14} />
          Refresh
        </button>
      </div>
      <State loading={models.loading} error={models.error || channels.error} />
      {models.data && (
        <section className="c-panel">
          {visible.length ? (
            <div className="c-table-wrap c-catalog-table">
              <table>
                <thead>
                  <tr>
                    <th>Public model / provider</th>
                    <th>USDC / M · input / output</th>
                    <th>Channels</th>
                    <th>Status</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {visible.map((m) => {
                    const mapped =
                      channels.data?.data.filter((ch) =>
                        Object.hasOwn(ch.model_map, m.id),
                      ) ?? []
                    return (
                      <tr key={m.id}>
                        <td>
                          <strong>{m.name}</strong>
                          <code>{m.id}</code>
                          <small>{m.vendor}</small>
                        </td>
                        <td>
                          {m.pricing_configured ? (
                            <>
                              <span>
                                {m.pricing?.mode === 'tiered'
                                  ? 'Context tiers'
                                  : m.pricing?.mode === 'expression'
                                    ? 'Expression price'
                                    : `${money(m.input_price)} / ${money(m.output_price)}`}
                              </span>
                            </>
                          ) : (
                            <span className="c-muted">Not configured</span>
                          )}
                        </td>
                        <td>
                          <div className="c-channel-models">
                            {mapped.length ? (
                              mapped.map((ch) => (
                                <a key={ch.id} href={channelLink(ch.id)}>
                                  {ch.name}
                                  {!ch.enabled ? ' · disabled' : ''}
                                </a>
                              ))
                            ) : (
                              <a href="#admin?section=channels">
                                Add channel mapping
                              </a>
                            )}
                          </div>
                        </td>
                        <td>
                          <Badge
                            tone={
                              !m.pricing_configured
                                ? 'amber'
                                : m.enabled
                                  ? 'lime'
                                  : ''
                            }
                          >
                            {!m.pricing_configured
                              ? 'Draft'
                              : m.enabled
                                ? 'Enabled'
                                : 'Disabled'}
                          </Badge>
                          {m.enabled && !mapped.some((ch) => ch.enabled) && (
                            <small>No enabled channel</small>
                          )}
                        </td>
                        <td>
                          <div className="c-row-actions">
                            <button
                              className="c-button c-quiet"
                              disabled={action.busy}
                              onClick={() => {
                                setEditing(m)
                                setShow(true)
                                action.clear()
                              }}
                            >
                              {m.pricing_configured ? 'Edit' : 'Configure'}
                            </button>
                            {m.pricing_configured && (
                              <button
                                className="c-button c-quiet"
                                disabled={action.busy}
                                onClick={() =>
                                  void action.run(async () => {
                                    await api('/api/admin/models', {
                                      method: 'PUT',
                                      body: JSON.stringify({
                                        ...m,
                                        enabled: !m.enabled,
                                      }),
                                    })
                                    models.reload()
                                    if (editing?.id === m.id) setShow(false)
                                    return m.enabled
                                      ? 'Model disabled.'
                                      : 'Model enabled.'
                                  })
                                }
                              >
                                {m.enabled ? 'Disable' : 'Enable'}
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              title={
                models.data.data.length
                  ? 'No matching models.'
                  : 'Build your model catalog.'
              }
            >
              {models.data.data.length
                ? 'Try another search or filter.'
                : 'Fetch models from an upstream channel, or add a public model manually.'}
            </Empty>
          )}
        </section>
      )}
    </>
  )
}
