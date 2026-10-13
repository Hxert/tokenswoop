import { useEffect, useRef, useState } from 'react'
import { api, money } from './api'
import type { Model, PricingConfig, PricingTrace, TokenRates } from './api'
import { Notice } from './ui'

const keys = [
  'input_price',
  'output_price',
  'cache_read_price',
  'cache_write_price',
  'cache_write_1h_price',
] as const
const labels = [
  'Input',
  'Output',
  'Cache read',
  'Cache write · 5m',
  'Cache write · 1h',
]
const zero: TokenRates = {
  input_price: '',
  output_price: '',
  cache_read_price: '',
  cache_write_price: '',
  cache_write_1h_price: '',
}
export function PriceTrace({ trace }: { trace?: PricingTrace | null }) {
  if (!trace) return null
  return (
    <div className="c-price-trace">
      <dl className="c-payment-addresses">
        <dt>Pricing rule</dt>
        <dd>
          {trace.mode}
          {trace.matched_tier ? ` · ${trace.matched_tier}` : ''}
        </dd>
        <dt>Token cost</dt>
        <dd>{money(trace.token_cost, 12)} USDC</dd>
        <dt>Hosted tools</dt>
        <dd>
          {money(trace.tool_cost, 12)} USDC{' '}
          {Object.entries(trace.facts.tool_calls ?? {})
            .map(([k, v]) => `${k} × ${v}`)
            .join(' · ')}
        </dd>
        <dt>Account discount</dt>
        <dd>{trace.discount_bps / 100}%</dd>
        <dt>Final amount</dt>
        <dd>{money(trace.amount, 12)} USDC</dd>
      </dl>
      <details>
        <summary>Calculation facts</summary>
        <pre className="c-json">{JSON.stringify(trace, null, 2)}</pre>
      </details>
    </div>
  )
}
export function PricingEditor({ model }: { model: Model | null }) {
  const fieldset = useRef<HTMLFieldSetElement>(null)
  const previewSequence = useRef(0)
  const [value, setValue] = useState<PricingConfig>(
    model?.pricing ?? { mode: 'standard' },
  )
  const [preview, setPreview] = useState<PricingTrace | null>(null),
    [error, setError] = useState(''),
    [busy, setBusy] = useState(false)
  const [input, setInput] = useState(10000),
    [output, setOutput] = useState(1000),
    [cached, setCached] = useState(0),
    [write, setWrite] = useState(0),
    [hour, setHour] = useState(0),
    [discount, setDiscount] = useState(0),
    [toolCalls, setToolCalls] = useState(0),
    [fileCalls, setFileCalls] = useState(0),
    [serviceTier, setServiceTier] = useState('')
  const activeValue: PricingConfig = {
    mode: value.mode ?? 'standard',
    tool_prices: value.tool_prices,
    ...(value.mode === 'tiered' ? { tiers: value.tiers } : {}),
    ...(value.mode === 'expression' ? { expression: value.expression } : {}),
  }
  useEffect(() => {
    const form = fieldset.current?.form
    const invalidate = () => {
      previewSequence.current++
      setPreview(null)
      setError('')
    }
    form?.addEventListener('input', invalidate)
    form?.addEventListener('change', invalidate)
    return () => {
      previewSequence.current++
      form?.removeEventListener('input', invalidate)
      form?.removeEventListener('change', invalidate)
    }
  }, [])
  const change = (next: PricingConfig) => {
    previewSequence.current++
    setValue(next)
    setPreview(null)
    setError('')
  }
  const tierChange = (index: number, patch: Record<string, string | number>) =>
    change({
      ...value,
      tiers: value.tiers?.map((t, i) => (i === index ? { ...t, ...patch } : t)),
    })
  return (
    <fieldset ref={fieldset}>
      <legend>Pricing rules</legend>
      <input type="hidden" name="pricing" value={JSON.stringify(activeValue)} />
      <div className="c-form-grid">
        <label>
          Pricing mode
          <select
            value={value.mode ?? 'standard'}
            onChange={(e) => {
              const form = e.currentTarget.form
              const fields = form ? new FormData(form) : null
              const initialRates = Object.fromEntries(
                keys.map((key) => [
                  key,
                  String(fields?.get(key) ?? model?.[key] ?? ''),
                ]),
              ) as TokenRates
              change({
                ...value,
                mode: e.target.value as PricingConfig['mode'],
                tiers:
                  e.target.value === 'tiered' && !value.tiers?.length
                    ? [
                        {
                          name: 'Standard context',
                          max_input_tokens: 128000,
                          ...initialRates,
                        },
                        {
                          name: 'Long context',
                          max_input_tokens: 0,
                          ...initialRates,
                        },
                      ]
                    : value.tiers,
              })
            }}
          >
            <option value="standard">Standard token rates above</option>
            <option value="tiered">Context length tiers</option>
            <option value="expression">Pricing expression</option>
          </select>
        </label>
      </div>
      {value.mode === 'tiered' && (
        <div className="c-price-tiers">
          <p className="c-muted">
            The confirmed total input, including cached tokens, selects one rate
            tier for the whole request. The last tier must have no upper bound
            (0).
          </p>
          {value.tiers?.map((t, i) => (
            <div className="c-price-tier" key={i}>
              <div className="c-form-grid">
                <label>
                  Tier name
                  <input
                    value={t.name}
                    required
                    maxLength={80}
                    onChange={(e) => tierChange(i, { name: e.target.value })}
                  />
                </label>
                <label>
                  Input upper bound · 0 = unlimited
                  <input
                    type="number"
                    value={t.max_input_tokens}
                    min={0}
                    required
                    onChange={(e) =>
                      tierChange(i, {
                        max_input_tokens: Number(e.target.value),
                      })
                    }
                  />
                </label>
                {keys.map((k, j) => (
                  <label key={k}>
                    {labels[j]} · USDC / 1M
                    <input
                      value={t[k]}
                      required
                      inputMode="decimal"
                      onChange={(e) => tierChange(i, { [k]: e.target.value })}
                    />
                  </label>
                ))}
              </div>
              <button
                type="button"
                className="c-button c-small c-secondary"
                onClick={() =>
                  change({
                    ...value,
                    tiers: value.tiers?.filter((_, n) => n !== i),
                  })
                }
              >
                Remove tier
              </button>
            </div>
          ))}
          <button
            type="button"
            className="c-button c-secondary"
            disabled={(value.tiers?.length ?? 0) >= 16}
            onClick={() =>
              change({
                ...value,
                tiers: [
                  ...(value.tiers ?? []),
                  {
                    name: `Tier ${(value.tiers?.length ?? 0) + 1}`,
                    max_input_tokens: 0,
                    ...zero,
                  },
                ],
              })
            }
          >
            Add tier
          </button>
        </div>
      )}
      {value.mode === 'expression' && (
        <>
          <label>
            Decimal pricing expression
            <textarea
              rows={4}
              required
              value={value.expression ?? ''}
              placeholder={
                'v1:len <= 128000 ? tier("standard", p*2+c*8) : tier("long", p*4+c*12)'
              }
              onChange={(e) => change({ ...value, expression: e.target.value })}
            />
          </label>
          <p className="c-muted">
            <code>p</code> input, <code>c</code> output, <code>len</code> total
            input. Referencing <code>cr</code>, <code>cc</code> or{' '}
            <code>cc1h</code> separates that cache category from p. Rates are
            USDC / 1M. <code>fixed(0.01)</code> adds 0.01 USDC per completed
            request. Supports min/max, abs, ceil/floor, conditions, tier labels
            and non-secret <code>param("service_tier")</code> controls.
          </p>
        </>
      )}
      <div className="c-form-grid">
        {(['web_search', 'file_search'] as const).map((tool) => (
          <label key={tool}>
            {tool === 'web_search' ? 'Web search' : 'File search'} · USDC /
            actual call
            <input
              inputMode="decimal"
              value={value.tool_prices?.[tool] ?? ''}
              placeholder="Disabled · leave blank"
              onChange={(e) => {
                const prices = { ...value.tool_prices }
                if (e.target.value === '') delete prices[tool]
                else prices[tool] = e.target.value
                change({ ...value, tool_prices: prices })
              }}
            />
          </label>
        ))}
      </div>
      <p className="c-muted">
        Tool fees require confirmed provider counters. Enter 0 explicitly to
        offer a supported tool free. Price previews do not charge your wallet.
      </p>
      <details className="c-price-preview">
        <summary>Test this price before saving</summary>
        <div className="c-form-grid">
          {(
            [
              [input, setInput, 'Total input'],
              [output, setOutput, 'Output'],
              [cached, setCached, 'Cache read'],
              [write, setWrite, 'Cache write · total'],
              [hour, setHour, 'Cache write · 1h subset'],
              [discount, setDiscount, 'Discount · basis points'],
              [toolCalls, setToolCalls, 'Web search calls'],
              [fileCalls, setFileCalls, 'File search calls'],
            ] as const
          ).map(([v, set, label]) => (
            <label key={label}>
              {label}
              <input
                type="number"
                min={0}
                max={label.includes('Discount') ? 10000 : undefined}
                value={v}
                onChange={(e) => {
                  set(Number(e.target.value))
                  setPreview(null)
                }}
              />
            </label>
          ))}
          <label>
            Effective service tier
            <input
              value={serviceTier}
              onChange={(e) => {
                setServiceTier(e.target.value)
                setPreview(null)
              }}
              placeholder="default / flex / priority"
            />
          </label>
        </div>
        <button
          type="button"
          className="c-button c-secondary"
          disabled={busy}
          onClick={async (e) => {
            const form = e.currentTarget.form
            if (!form) return
            const f = new FormData(form)
            const sequence = ++previewSequence.current
            setBusy(true)
            setError('')
            setPreview(null)
            try {
              const result = await api<{ trace: PricingTrace }>(
                '/api/admin/pricing/preview',
                {
                  method: 'POST',
                  body: JSON.stringify({
                    model: {
                      ...Object.fromEntries(keys.map((k) => [k, f.get(k)])),
                      pricing: activeValue,
                    },
                    discount_bps: discount,
                    facts: {
                      input_tokens: input,
                      output_tokens: output,
                      cache_read_tokens: cached,
                      cache_write_tokens: write,
                      cache_write_1h_tokens: hour,
                      tool_calls: {
                        web_search: toolCalls,
                        file_search: fileCalls,
                      },
                      parameters: { service_tier: serviceTier },
                    },
                  }),
                },
              )
              if (sequence === previewSequence.current) setPreview(result.trace)
            } catch (err) {
              if (sequence === previewSequence.current)
                setError((err as Error).message)
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Calculating…' : 'Calculate sample'}
        </button>
        {error && <Notice error>{error}</Notice>}
        <PriceTrace trace={preview} />
        <p className="c-muted">
          Hypothetical usage only. This calculator does not invoke a model or
          charge an account.
        </p>
      </details>
    </fieldset>
  )
}

export function PublicModelPricing({ model }: { model: Model }) {
  const p = model.pricing
  const rates = (r: TokenRates) => (
    <>
      <div className="c-model-prices">
        <div>
          <span>Input / 1M</span>
          <strong>${money(r.input_price, 8)}</strong>
        </div>
        <div>
          <span>Output / 1M</span>
          <strong>${money(r.output_price, 8)}</strong>
        </div>
      </div>
      <dl className="c-model-details">
        <div>
          <dt>Cache read / 1M</dt>
          <dd>${money(r.cache_read_price, 8)}</dd>
        </div>
        <div>
          <dt>Cache write · 5m / 1h / 1M</dt>
          <dd>
            ${money(r.cache_write_price, 8)} / $
            {money(r.cache_write_1h_price, 8)}
          </dd>
        </div>
      </dl>
    </>
  )
  return (
    <div>
      {p?.mode === 'expression' ? (
        <>
          <p>Expression pricing · confirmed usage</p>
          <pre className="c-json">{p.expression}</pre>
          <p className="c-muted">
            p = input, c = output, len = total input; cr / cc / cc1h separate
            cache read / write / 1h write. Rates per 1M tokens. fixed() is USDC
            per completed request.
          </p>
        </>
      ) : p?.mode === 'tiered' ? (
        <>
          {p.tiers?.map((t, i) => (
            <details key={t.name} open={i === 0}>
              <summary>
                {t.name} ·{' '}
                {t.max_input_tokens
                  ? `up to ${t.max_input_tokens.toLocaleString()} input tokens`
                  : 'above prior bound'}
              </summary>
              {rates(t)}
            </details>
          ))}
        </>
      ) : (
        rates(model)
      )}
      {Object.entries(p?.tool_prices ?? {}).map(([tool, price]) => (
        <p className="c-muted" key={tool}>
          {tool}: ${money(price, 8)} / confirmed call
        </p>
      ))}
    </div>
  )
}
