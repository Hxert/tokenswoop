import { network } from './network'
import { useEffect, useState } from 'react'
import { ArrowUpRight, RefreshCw, Wallet } from 'lucide-react'
import { api, date, money, short, useResource } from './api'
import type { Me, Usage } from './api'
import {
  Badge,
  CopyButton,
  CopyValue,
  Empty,
  Modal,
  Notice,
  PageTitle,
  State,
} from './ui'
import { PriceTrace } from './Pricing'
import { useWalletConnection } from './WalletProvider'
import TestnetFaucet from './TestnetFaucet'

export type BillingStatus = {
  faucet?: { enabled: boolean }
  enabled: boolean
  chain_id: number
  decimals: number
  symbol: string
  network: string
  contract?: string
  token?: string
  provider?: string
  recipient?: string
  calls_blocked?: boolean
  settlement_threshold?: string
  settlement_window_seconds?: number
  worker_ready: boolean
  chain_error?: string
  wallet?: {
    balance_units: string
    allowance_units: string
    block_number: string
  }
  summary: {
    outstanding: string
    paid: string
    pending: string
    blocked: string
  }
}
type Bill = {
  id: string
  amount: string
  amount_units: string
  state: string
  error_code: string
  chain_id: number
  contract: string
  token: string
  provider: string
  recipient: string
  wallet: string
  tx_hash: string | null
  block_number: string | null
  created_at: string
  paid_at: string | null
  request_count: number
}
type BillDetail = {
  bill: Bill
  items: {
    request_id: string
    model_id: string
    amount: string
    request_amount: string
    price_snapshot: { price_version: number }
    tier_snapshot: { discount_bps: number }
  }[]
  attempts: { tx_hash: string; state: string; created_at: string }[]
}
const explorer = () => network().explorer
const reasons: Record<string, string> = {
  insufficient_balance:
    'Add payment tokens to your wallet, then retry this bill.',
  insufficient_allowance:
    'Increase your payment allowance, then retry this bill.',
  transaction_reverted:
    'The transaction reverted. Check your balance and allowance, then retry.',
  signer_unavailable:
    'The settlement service is retrying. Your bill has been saved.',
  configuration_changed:
    'This bill requires operator review of its payment contract.',
  external_payment_requires_review:
    'An existing chain payment requires operator reconciliation.',
}
function units(value: string) {
  const n = BigInt(value)
  return `${n / 1000000n}.${(n % 1000000n).toString().padStart(6, '0')}`
}
function tokenUnits(value: string) {
  if (!/^\d+(\.\d{1,6})?$/.test(value))
    throw new Error('Enter an amount with at most 6 decimal places.')
  const [whole, fraction = ''] = value.split('.')
  const n = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'))
  if (n <= 0n || n > 100000000n)
    throw new Error(
      `Choose an allowance greater than 0 and at most 100 ${network().symbol}.`,
    )
  return n
}
export default function Billing({
  me,
  refresh,
}: {
  me: Me
  refresh: () => Promise<void>
}) {
  const wallet = useWalletConnection()
  const status = useResource<BillingStatus>('/api/billing', true, true),
    [offset, setOffset] = useState(0),
    bills = useResource<{ data: Bill[]; has_more: boolean }>(
      `/api/billing/bills?offset=${offset}`,
      true,
      true,
    )
  const [amount, setAmount] = useState('10'),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [message, setMessage] = useState(''),
    [txHash, setTxHash] = useState('')
  const [selected, setSelected] = useState(
    new URLSearchParams(location.hash.split('?')[1]).get('bill') ?? '',
  )
  const detail = useResource<BillDetail>(
    `/api/billing/bills/${selected}`,
    !!selected,
    true,
  )
  const data = status.data
  const reload = () => {
    status.reload()
    bills.reload()
    detail.reload()
    void refresh()
  }
  useEffect(() => {
    const refreshed = () => {
      status.reload()
      bills.reload()
      detail.reload()
      void refresh()
    }
    window.addEventListener('tokenswoop:wallet-transaction', refreshed)
    const id = setInterval(() => {
      status.reload()
      bills.reload()
      if (selected) detail.reload()
    }, 15000)
    return () => {
      clearInterval(id)
      window.removeEventListener('tokenswoop:wallet-transaction', refreshed)
    }
  }, [status.reload, bills.reload, detail.reload, selected])
  const check = async () => {
    setBusy(true)
    setError('')
    try {
      await api('/api/billing/refresh', { method: 'POST' })
      reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const approve = async (revoke = false) => {
    setBusy(true)
    setError('')
    setMessage('')
    try {
      if (
        !data?.enabled ||
        !data.token ||
        !data.contract ||
        data.chain_id !== network().chainID
      )
        throw new Error('Wallet payment is unavailable. Refresh and try again.')
      const limit = revoke ? 0n : tokenUnits(amount)
      const calldata =
        '0x095ea7b3' +
        data.contract.slice(2).padStart(64, '0') +
        limit.toString(16).padStart(64, '0')
      const hash = await wallet.send(me.user.wallet, {
        action: 'payment_approve',
        amount_units: limit.toString(),
        to: data.token,
        data: calldata,
        title: revoke
          ? 'Revoke payment allowance'
          : 'Approve payment allowance',
        description: revoke
          ? 'Remove the payment contract’s remaining token allowance.'
          : 'Set the payment contract allowance to ' +
            units(limit.toString()) +
            ` ${network().symbol}. This replaces its remaining allowance. API usage is charged only after completion.`,
      })
      setTxHash(hash)
      if (
        wallet.passkey?.(me.user.wallet) &&
        wallet.sponsored(me.user.wallet)
      ) {
        await api('/api/billing/refresh', { method: 'POST' })
        reload()
        return
      }
      setMessage('Authorization submitted. Waiting for confirmation…')
      for (let i = 0; i < 20; i++) {
        const receipt = await wallet.receipt(hash)
        if (receipt) {
          if (BigInt(receipt.status) !== 1n)
            throw new Error(
              'Authorization reverted. Your previous allowance remains in effect.',
            )
          setMessage(
            'Authorization confirmed. Wallet status updates after chain finality.',
          )
          await api('/api/billing/refresh', { method: 'POST' })
          reload()
          return
        }
        await new Promise((resolve) => setTimeout(resolve, 1500))
      }
      setMessage(
        'Authorization is still confirming. Follow the transaction link and refresh after confirmation.',
      )
    } catch (e) {
      if (!(e as { notified?: boolean }).notified)
        setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const retry = async (id: string) => {
    setBusy(true)
    setError('')
    try {
      await api(`/api/billing/bills/${id}/retry`, { method: 'POST' })
      setMessage(
        'Retry requested for the same bill. Payment conditions will be checked again.',
      )
      reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <PageTitle
        title="Wallet & billing"
        description="Completed usage becomes a bill. Your wallet pays the confirmed amount."
        action={
          <button
            className="c-button"
            disabled={busy}
            onClick={data?.enabled ? check : reload}
          >
            <RefreshCw size={15} /> Refresh
          </button>
        }
      />
      <State loading={status.loading && !data} error={status.error} />
      {error && !selected && <Notice error>{error}</Notice>}
      {message && !selected && (
        <Notice>
          {message}
          {txHash && (
            <>
              {' '}
              <a
                href={`${explorer()}/tx/${txHash}`}
                target="_blank"
                rel="noreferrer"
              >
                View transaction ↗
              </a>
            </>
          )}
        </Notice>
      )}
      {data && (
        <>
          <div className="c-billing-metrics">
            <section className="c-panel">
              <small>Wallet paid · {network().symbol}</small>
              <strong>{money(data.summary.paid, 12)}</strong>
            </section>
            <section className="c-panel">
              <small>Awaiting settlement / carried forward</small>
              <strong>{money(data.summary.pending)}</strong>
            </section>
            <section className="c-panel">
              <small>Payment needs attention</small>
              <strong>{money(data.summary.blocked, 12)}</strong>
            </section>
          </div>
          {!data.enabled ? (
            <Notice>
              Wallet payments are disabled for this workspace. New paid API
              calls are unavailable.
            </Notice>
          ) : (
            <section className="c-panel c-payment-settings">
              <div className="c-panel-title">
                <div>
                  <h2>
                    <Wallet size={20} /> Wallet payment
                  </h2>
                  <p>
                    {data.network} · chain {data.chain_id}
                    {network().testnet && ' · test tokens only'}
                  </p>
                </div>
                <Badge tone={data.worker_ready ? 'lime' : ''}>
                  {data.worker_ready
                    ? 'Settlement service online'
                    : 'Settlement service unavailable'}
                </Badge>
              </div>
              {data.chain_error && <Notice error>{data.chain_error}</Notice>}
              <div className="c-payment-wallet">
                <span>
                  {wallet.passkey?.(me.user.wallet)
                    ? 'Passkey wallet'
                    : wallet.embedded(me.user.wallet)
                      ? 'Embedded wallet'
                      : 'Payment wallet'}
                </span>
                <CopyValue value={me.user.wallet} label="Copy wallet address" />
                {wallet.passkey?.(me.user.wallet) && (
                  <button
                    className="c-button c-small"
                    disabled={wallet.busy}
                    onClick={() => wallet.backup?.(me.user.wallet)}
                  >
                    Back up wallet
                  </button>
                )}
                {wallet.sponsored(me.user.wallet) && (
                  <Badge tone="lime">Gas sponsored</Badge>
                )}
              </div>
              <div className="c-payment-balances">
                <span>
                  Wallet balance
                  <strong>
                    {data.wallet ? units(data.wallet.balance_units) : '—'}{' '}
                    {network().symbol}
                  </strong>
                </span>
                <span>
                  Remaining allowance
                  <strong>
                    {data.wallet ? units(data.wallet.allowance_units) : '—'}{' '}
                    {network().symbol}
                  </strong>
                </span>
              </div>
              <p>
                Authorize a spending limit once. Tokens stay in your wallet
                until completed requests are settled onchain.
              </p>
              <div className="c-payment-actions">
                <label>
                  New allowance · {network().symbol}
                  <input
                    inputMode="decimal"
                    maxLength={16}
                    value={amount}
                    onChange={(e) => setAmount(e.target.value)}
                    disabled={busy || wallet.busy}
                  />
                </label>
                <button
                  className="c-button c-primary"
                  disabled={busy || wallet.busy}
                  onClick={() => approve()}
                >
                  {busy ? 'Working…' : 'Approve allowance'}{' '}
                  <ArrowUpRight size={15} />
                </button>
                <button
                  className="c-button"
                  disabled={
                    busy ||
                    wallet.busy ||
                    !data.wallet ||
                    data.wallet.allowance_units === '0'
                  }
                  onClick={() => approve(true)}
                >
                  Revoke allowance
                </button>
              </div>
              <p className="c-muted">
                Setting a new allowance replaces the remaining limit.
              </p>
              <details>
                <summary>Collection policy</summary>
                <p>
                  Calls continue while bills wait or confirm onchain, provided
                  your payment allowance covers outstanding usage. A settlement
                  failure due to insufficient balance or allowance pauses new
                  paid calls until the failed bill is paid. Calls already in
                  progress will finish. Charges below 0.000001{' '}
                  {network().symbol} accumulate without rounding up.
                </p>
              </details>
            </section>
          )}
        </>
      )}
      {network().testnet && data && (
        <TestnetFaucet
          key={me.user.wallet}
          enabled={!!data.faucet?.enabled}
          account={me.user.wallet}
          onReceived={() => {
            void api('/api/billing/refresh', { method: 'POST' })
              .catch(() => {})
              .finally(reload)
          }}
        />
      )}
      <section className="c-panel">
        <div className="c-panel-title">
          <div>
            <h2>Bills</h2>
            <p>
              Every collection links back to its requests and onchain receipt.
            </p>
          </div>
        </div>
        <State loading={bills.loading} error={bills.error} />
        {bills.data &&
          (bills.data.data.length ? (
            <div className="c-table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Bill</th>
                    <th>Amount · {network().symbol}</th>
                    <th>Status</th>
                    <th>Transaction</th>
                    <th>Created</th>
                  </tr>
                </thead>
                <tbody>
                  {bills.data.data.map((b) => (
                    <tr key={b.id}>
                      <td>
                        <span className="c-bill-id">
                          <button
                            type="button"
                            className="c-bill-link"
                            aria-label={`View bill ${b.id}`}
                            title={b.id}
                            onClick={() => {
                              setSelected(b.id)
                              setError('')
                              setMessage('')
                            }}
                          >
                            {short(b.id)}
                          </button>
                          <CopyButton value={b.id} label="Copy bill ID" />
                        </span>
                        <small>{b.request_count} requests</small>
                      </td>
                      <td>{money(b.amount)}</td>
                      <td>
                        <Badge
                          tone={
                            b.state === 'paid'
                              ? 'lime'
                              : b.state === 'blocked' || b.state === 'review'
                                ? 'red'
                                : ''
                          }
                        >
                          {b.state === 'submitted' ? 'Confirming' : b.state}
                        </Badge>
                        {b.error_code && (
                          <small>{reasons[b.error_code] ?? b.error_code}</small>
                        )}
                      </td>
                      <td>
                        {b.tx_hash ? (
                          <a
                            href={`${explorer()}/tx/${b.tx_hash}`}
                            target="_blank"
                            rel="noreferrer"
                          >
                            {short(b.tx_hash)} ↗
                          </a>
                        ) : (
                          '—'
                        )}
                      </td>
                      <td>{date(b.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty title="No bills yet">
              Confirmed usage will appear here once wallet charges reach
              0.000001 {network().symbol}.
            </Empty>
          ))}
        <div className="c-payment-actions">
          <button
            className="c-button"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - 50))}
          >
            Previous
          </button>
          <button
            className="c-button"
            disabled={!bills.data?.has_more}
            onClick={() => setOffset(offset + 50)}
          >
            Next
          </button>
        </div>
      </section>
      {selected && (
        <Modal title="Bill detail" wide onClose={() => setSelected('')}>
          <div className="c-bill-detail">
            <CopyValue
              className="c-long-id"
              value={selected}
              label="Copy bill ID"
            />
            {error && <Notice error>{error}</Notice>}
            {message && <Notice>{message}</Notice>}
            <State loading={detail.loading} error={detail.error} />
            {detail.data?.bill.id === selected && (
              <>
                <p>
                  {money(detail.data.bill.amount)} {network().symbol} ·{' '}
                  {detail.data.bill.state}
                  {detail.data.bill.paid_at &&
                    ` · ${date(detail.data.bill.paid_at)}`}
                </p>
                {detail.data.bill.error_code && (
                  <Notice error>
                    {reasons[detail.data.bill.error_code] ??
                      detail.data.bill.error_code}
                  </Notice>
                )}
                {['blocked', 'queued'].includes(detail.data.bill.state) &&
                  data?.enabled && (
                    <button
                      className="c-button c-primary"
                      disabled={busy}
                      onClick={() => retry(selected)}
                    >
                      Retry this bill
                    </button>
                  )}
                <div className="c-table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Request</th>
                        <th>Model / discount</th>
                        <th>Allocated to bill</th>
                        <th>Full request cost</th>
                      </tr>
                    </thead>
                    <tbody>
                      {detail.data.items.map((i) => (
                        <tr key={i.request_id}>
                          <td>
                            <a href={`#receipt?request=${i.request_id}`}>
                              {short(i.request_id)}
                            </a>
                          </td>
                          <td>
                            {i.model_id}
                            <small>
                              {i.tier_snapshot.discount_bps / 100}% discount
                            </small>
                          </td>
                          <td>{money(i.amount, 12)}</td>
                          <td>{money(i.request_amount, 12)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <h3>Transaction history</h3>
                {detail.data.attempts.length ? (
                  detail.data.attempts.map((a) => (
                    <p key={a.tx_hash}>
                      <a
                        href={`${explorer()}/tx/${a.tx_hash}`}
                        target="_blank"
                        rel="noreferrer"
                      >
                        {short(a.tx_hash)} ↗
                      </a>{' '}
                      · {a.state} · {date(a.created_at)}
                    </p>
                  ))
                ) : (
                  <p>No transaction has been signed yet.</p>
                )}
              </>
            )}
          </div>
        </Modal>
      )}
    </>
  )
}
export function RequestReceipt() {
  const id =
    new URLSearchParams(location.hash.split('?')[1]).get('request') ?? ''
  const receipt = useResource<
    Usage & {
      attempts?: {
        status: number
        outcome: string
        diagnostics: string[]
        created_at: string
      }[]
      bills: {
        id: string
        state: string
        amount: string
        tx_hash: string | null
      }[]
    }
  >(`/api/requests/${id}`, /^[0-9a-f-]{36}$/.test(id))
  return (
    <>
      <PageTitle
        eyebrow="REQUEST RECEIPT"
        title="Usage & payment"
        description="The completed request cost and each wallet collection."
        action={
          <a className="c-button" href="#usage">
            Back to usage
          </a>
        }
      />
      <State loading={receipt.loading} error={receipt.error} />
      {receipt.data && (
        <section className="c-panel c-payment-settings">
          <CopyValue className="c-long-id" value={id} label="Copy request ID" />
          <p>
            {receipt.data.model_id} · {receipt.data.state} ·{' '}
            {date(receipt.data.created_at)}
          </p>
          <dl className="c-payment-addresses">
            <dt>Actual cost</dt>
            <dd>{money(receipt.data.amount, 12)} USDC</dd>
            <dt>Wallet paid</dt>
            <dd>{money(receipt.data.wallet_paid_amount, 12)}</dd>
            {Number(receipt.data.waived_amount ?? 0) > 0 && (
              <>
                <dt>Waived</dt>
                <dd>{money(receipt.data.waived_amount!, 18)}</dd>
              </>
            )}
            <dt>Outstanding</dt>
            <dd>{money(receipt.data.unpaid_amount, 12)}</dd>
          </dl>
          <dl className="c-payment-addresses">
            <dt>Actual input</dt>
            <dd>{receipt.data.input_tokens} tokens</dd>
            <dt>First token</dt>
            <dd>
              {receipt.data.first_token_ms == null
                ? '—'
                : `${receipt.data.first_token_ms} ms`}
            </dd>
            <dt>Generation speed</dt>
            <dd>
              {receipt.data.generation_ms
                ? `${((receipt.data.output_tokens * 1000) / receipt.data.generation_ms).toFixed(2)} tok/s`
                : '—'}
            </dd>
          </dl>
          <PriceTrace trace={receipt.data.pricing_trace} />
          {!!receipt.data.attempts?.length && (
            <details>
              <summary>Request attempts & compatibility</summary>
              {receipt.data.attempts.map((attempt, index) => (
                <div key={index}>
                  <p>
                    {index + 1}. {attempt.outcome.replaceAll('_', ' ')} ·{' '}
                    {attempt.status || 'No response'}
                  </p>
                  {!!attempt.diagnostics.length && (
                    <p className="c-muted">
                      {attempt.diagnostics
                        .map((code) => code.replaceAll('_', ' '))
                        .join(' · ')}
                    </p>
                  )}
                </div>
              ))}
            </details>
          )}
          <h3>Associated bills</h3>
          {receipt.data.bills.length ? (
            receipt.data.bills.map((b) => (
              <p key={b.id}>
                <a href={`#billing?bill=${b.id}`}>{short(b.id)}</a> · {b.state}{' '}
                · {money(b.amount, 12)} {network().symbol}
              </p>
            ))
          ) : (
            <p>
              No wallet bill allocated yet. Outstanding amounts below one token
              unit carry forward to the next settlement.
            </p>
          )}
        </section>
      )}
    </>
  )
}
