import { useCallback, useEffect, useRef, useState } from 'react'

export type User = {
  id: string
  wallet: string
  role: 'user' | 'admin'
  status: string
  tier: string
  tier_override: boolean
  unpaid_balance: string
}
export type Tier = {
  id: string
  discount_bps: number
  rpm: number
  concurrency: number
  min_principal: string | null
  version: number
  settlement_threshold: string
}
export type Me = { user: User; tier: Tier; settlement_enabled: boolean }
export type Model = {
  pricing?: PricingConfig
  id: string
  name: string
  vendor: string
  enabled: boolean
  pricing_configured: boolean
  tools: boolean
  structured_output: boolean
  price_version: number
  input_price: string
  output_price: string
  cache_read_price: string
  cache_write_price: string
  cache_write_1h_price: string
}
export type APIKey = {
  id: string
  name: string
  prefix: string
  status: string
  allowed_models: string[]
  budget: string | null
  spent: string
  expires_at: string | null
  created_at: string
}
export type Usage = {
  pricing_trace?: PricingTrace | null
  first_token_ms?: number | null
  generation_ms?: number | null
  id: string
  model_id: string
  key_id: string | null
  state: string
  amount: string
  wallet_paid_amount: string
  unpaid_amount: string
  waived_amount?: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  price_version: number
  latency_ms: number
  error_code: string
  created_at: string
}
export type Stats = {
  summary: {
    requests: number
    completed: number
    pending: number
    spent: string
    waived: string
    wallet_paid: string
    unpaid: string
    input_tokens: number
    output_tokens: number
    avg_latency_ms: number
  }
  daily: {
    date: string
    requests: number
    spent: string
    input_tokens: number
    output_tokens: number
  }[]
}
export type Config = {
  chain_id: number
  settlement_enabled: boolean
  environment: string
  privy?: { enabled: boolean; app_id: string; gas_sponsorship: boolean }
  mera?: {
    gas_sponsorship: boolean
    implementation: string
    token: string
    payment: string
    reserve: string
  }
}
export type ChannelOptions = {
  file_search_stores?: string[]
  allow_stored_responses?: boolean
  supports_compact?: boolean
  supports_alpha_search?: boolean
  dialect?: string
  preserve_model_suffix?: boolean
  thinking_budget_ratio?: number
  gemini_thought_signature?: boolean
  allow_service_tier?: boolean
  omit_stream_options?: boolean
  thinking_to_content?: boolean
  http_protocol?: string
  http2_shards?: number
  request_timeout_seconds?: number
  stream_idle_seconds?: number
  weight?: number
  cache_affinity?: boolean
  headers?: Record<string, string>
  parameter_overrides?: Record<string, unknown>
  drop_parameters?: string[]
}
export type Channel = {
  options?: ChannelOptions
  id: string
  name: string
  protocol: string
  base_url: string
  model_map: Record<string, string>
  priority: number
  revision: number
  enabled: boolean
  failure_count: number
  disabled_until: string | null
  last_error: string
}
export type Health = {
  database: string
  redis_ready: boolean
  worker_ready: boolean
  worker_last_seen: string | null
  settlement_enabled: boolean
  settlement_ready: boolean
  settlement_last_seen: string | null
  queued_bills: number
  blocked_bills: number
  review_bills: number
  unknown_requests: number
  outbox_pending: number
  cooling_channels: number
  requests_24h: number
  failed_or_unknown_24h: number
  avg_latency_ms_24h: number
}
export type Audit = {
  id: string
  action: string
  target: string
  actor_id: string
  created_at: string
}
export type Unknown = {
  id: string
  wallet: string
  model_id: string
  error_code: string
  created_at: string
}

export class APIError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}
export function csrf() {
  return (
    document.cookie
      .split('; ')
      .find((v) => v.startsWith('ts_csrf='))
      ?.slice(8) ?? ''
  )
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(path, {
    ...options,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      'X-CSRF-Token': csrf(),
      ...options.headers,
    },
  })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new APIError(
      response.status,
      body?.error?.code ?? 'request_failed',
      body?.error?.message ?? `Request failed (${response.status}).`,
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
export function useResource<T>(
  path: string,
  enabled = true,
  retainOnRefresh = false,
) {
  const loadedPath = useRef(path)
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(''),
    [loading, setLoading] = useState(enabled),
    [version, setVersion] = useState(0)
  const reload = useCallback(() => setVersion((v) => v + 1), [])
  useEffect(() => {
    if (!enabled) {
      setData(null)
      setLoading(false)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    setError('')
    if (!retainOnRefresh || loadedPath.current !== path) setData(null)
    loadedPath.current = path
    api<T>(path, { signal: controller.signal })
      .then(setData)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [path, enabled, version, retainOnRefresh])
  return { data, error, loading, reload }
}
export function money(value: string | number = 0, digits = 6) {
  return Number(value).toLocaleString('en-US', {
    minimumFractionDigits: Math.min(2, digits),
    maximumFractionDigits: digits,
  })
}
export function short(value: string) {
  return `${value.slice(0, 6)}…${value.slice(-4)}`
}
export function date(value: string) {
  return new Date(value).toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

declare global {
  interface Window {
    ethereum?: {
      request: (args: {
        method: string
        params?: unknown[]
      }) => Promise<unknown>
    }
  }
}
export async function walletLogin() {
  if (!window.ethereum)
    throw new Error(
      'Open this site in a browser with an EVM wallet extension, then try again.',
    )
  const wallets = (await window.ethereum.request({
    method: 'eth_requestAccounts',
  })) as string[]
  const wallet = wallets[0]
  if (!wallet) throw new Error('No wallet account selected.')
  return loginWithWalletSignature(wallet, async (message) => {
    const hex =
      '0x' +
      [...new TextEncoder().encode(message)]
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('')
    return (await window.ethereum!.request({
      method: 'personal_sign',
      params: [hex, wallet],
    })) as string
  })
}
export async function loginWithWalletSignature(
  wallet: string,
  sign: (message: string) => Promise<string>,
  source: 'external' | 'mera' = 'external',
) {
  const challenge = await api<{ id: string; message: string }>(
    '/api/auth/challenge',
    { method: 'POST', body: JSON.stringify({ wallet, source }) },
  )
  const signature = await sign(challenge.message)
  return api<User>('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({ id: challenge.id, signature }),
  })
}

export type TokenRates = {
  input_price: string
  output_price: string
  cache_read_price: string
  cache_write_price: string
  cache_write_1h_price: string
}
export type PricingConfig = {
  mode?: 'standard' | 'tiered' | 'expression'
  tiers?: (TokenRates & { name: string; max_input_tokens: number })[]
  expression?: string
  tool_prices?: { web_search?: string; file_search?: string }
}
export type PricingTrace = {
  mode: string
  matched_tier?: string
  expression_hash?: string
  token_cost: string
  tool_cost: string
  amount: string
  discount_bps: number
  facts: {
    input_tokens: number
    output_tokens: number
    cache_read_tokens: number
    cache_write_tokens: number
    cache_write_1h_tokens: number
    tool_calls?: Record<string, number>
    parameters?: Record<string, unknown>
  }
}
