import { network } from './network'
import { useEffect, useRef, useState } from 'react'
import {
  ArrowDownLeft,
  ArrowUpRight,
  Clock3,
  Layers,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'
import { api, date, money, short, useResource } from './api'
import type { Me, Tier } from './api'
import { Badge, Modal, Notice, PageTitle, State } from './ui'
import {
  formatReserveUnits as units,
  reserveCall,
  reserveExplorer as explorer,
  reserveUnits,
} from './reserveWallet'
import type { ReserveAction } from './reserveWallet'
import { useWalletConnection } from './WalletProvider'

export type ReserveSnapshot = {
  debt_units?: string
  frozen_units?: string
  withdrawable_units?: string
  debt_epoch?: string
  principal_units: string
  balance_units: string
  allowance_units: string
  paused: boolean
  block_number: string
  block_hash: string
}
export type ReserveStatus = {
  enabled: boolean
  mode: 'debt'
  manager?: string
  reports?: { tx_hash: string; state: string; created_at: string }[]
  chain_id: number
  symbol: string
  network: string
  contract: string
  token: string
  operator: string
  custody?: 'aave' | 'vault'
  testnet_mock: boolean
  snapshot: ReserveSnapshot
  tier: Tier
  tier_override: boolean
  worker_ready: boolean
  clearance: {
    in_flight: number
    usage_unknown: number
    open_bills: number
    unpaid: string
  }
}
const tierName = (id: string) => id[0].toUpperCase() + id.slice(1)
type Pending = {
  action: ReserveAction
  hash?: string
  operationId?: string
  started: number
}
const actions: Record<ReserveAction, string> = {
  approve: 'Reserve allowance',
  deposit: 'Deposit',
  withdraw: 'Withdrawal',
}

export default function Reserve({
  me,
  refresh,
}: {
  me: Me
  refresh: () => Promise<void>
}) {
  const wallet = useWalletConnection()
  const status = useResource<ReserveStatus>('/api/reserve', true, true)
  const tiers = useResource<{ data: Tier[] }>('/api/tiers', true, true)
  const data = status.data,
    snapshot = data?.enabled ? data.snapshot : null
  const symbol = data?.symbol ?? network().symbol
  const [dialog, setDialog] = useState<'deposit' | 'withdraw' | null>(null)
  const [amount, setAmount] = useState('50'),
    [working, setWorking] = useState(false),
    [error, setError] = useState(''),
    [message, setMessage] = useState('')
  const storageKey = `tokenswoop:reserve:${network().testnet ? '' : `${network().chainID}:`}${me.user.wallet.toLowerCase()}:${data?.contract ?? ''}`
  const [pending, setPending] = useState<Pending | null>(null)
  const transactionActive = useRef(false),
    live = useRef(true)
  const sync = () => {
    status.reload()
    tiers.reload()
    void refresh()
  }
  const syncRef = useRef(sync)
  syncRef.current = sync
  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
    }
  }, [])
  useEffect(() => {
    let restored: Pending | null = null
    try {
      const v = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
      if (
        v &&
        v.action in actions &&
        typeof v.started === 'number' &&
        (!v.hash || /^0x[0-9a-fA-F]{64}$/.test(v.hash))
      )
        restored = v
    } catch {
      /* Invalid local journal has no authority over chain state. */
    }
    setPending(restored)
  }, [storageKey])
  const remember = (p: Pending | null) => {
    if (p) localStorage.setItem(storageKey, JSON.stringify(p))
    else localStorage.removeItem(storageKey)
    if (live.current) setPending(p)
  }
  useEffect(() => {
    const refreshed = () => syncRef.current()
    window.addEventListener('tokenswoop:wallet-transaction', refreshed)
    const timer = window.setInterval(() => {
      status.reload()
    }, 10_000)
    return () => {
      clearInterval(timer)
      window.removeEventListener('tokenswoop:wallet-transaction', refreshed)
    }
  }, [status.reload])
  const previousTier = useRef('')
  useEffect(() => {
    const value = data?.enabled ? `${data.tier.id}:${data.tier.version}` : ''
    if (value && value !== previousTier.current) {
      previousTier.current = value
      void refresh()
    }
  }, [data?.tier?.id, data?.tier?.version, refresh])
  useEffect(() => {
    if (!pending?.hash && !pending?.operationId) return
    let stopped = false,
      checking = false
    const check = async () => {
      if (checking) return
      checking = true
      try {
        if (!pending.hash && pending.operationId) {
          const operation = await api<{ hash?: string; state: string }>(
            `/api/wallet/transactions/${pending.operationId}`,
          )
          if (stopped) return
          if (
            ['rejected', 'expired', 'cancelled', 'reverted'].includes(
              operation.state,
            )
          ) {
            remember(null)
            setError(
              'The wallet transaction was rejected. Review the operation before retrying.',
            )
            return
          }
          if (operation.hash) remember({ ...pending, hash: operation.hash })
          return
        }
        const receipt = await wallet.receipt(pending.hash!)
        if (stopped || !receipt) return
        if (receipt.status !== '0x1' && receipt.status !== '0x0') return
        const success = receipt.status === '0x1'
        remember(null)
        setMessage(
          success
            ? `${actions[pending.action]} confirmed. Your principal and tier update after chain finality.`
            : '',
        )
        setError(
          success
            ? ''
            : `${actions[pending.action]} reverted. Check your wallet and reserve status before retrying.`,
        )
        if (success && pending.action !== 'approve') setDialog(null)
        syncRef.current()
      } catch {
        /* Keep the saved hash on an RPC error; never resend automatically. */
      } finally {
        checking = false
      }
    }
    void check()
    const timer = window.setInterval(() => void check(), 2500)
    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [pending?.hash, pending?.operationId, storageKey])
  const busy = working || !!pending || wallet.busy
  const currentTier = data?.enabled ? data.tier : me.tier
  const principal = snapshot ? BigInt(snapshot.principal_units) : 0n
  const next = tiers.data?.data.find(
    (t) =>
      t.min_principal !== null &&
      Number(t.min_principal) * 1e6 > Number(principal),
  )
  let entered = 0n
  try {
    entered = reserveUnits(amount)
  } catch {
    /* Validate with an actionable message when submitting. */
  }
  const needsAllowance = snapshot && entered > BigInt(snapshot.allowance_units)
  const transaction = async (action: ReserveAction) => {
    if (!data?.enabled || busy || status.error || transactionActive.current)
      return
    transactionActive.current = true
    setWorking(true)
    setError('')
    setMessage('')
    try {
      const fresh = await api<ReserveStatus>('/api/reserve/refresh', {
        method: 'POST',
      })
      const v = fresh.snapshot
      const n = reserveUnits(amount)
      if (['approve', 'deposit'].includes(action) && v.paused)
        throw new Error('Deposits are temporarily paused.')
      if (action === 'approve' && n > BigInt(v.balance_units))
        throw Object.assign(
          new Error(
            `Insufficient wallet balance. You have ${units(v.balance_units)} ${symbol}; the approval amount is ${units(n.toString())} ${symbol}.`,
          ),
          { beforeSubmission: true },
        )
      if (
        action === 'deposit' &&
        (n > BigInt(v.balance_units) || n > BigInt(v.allowance_units))
      )
        throw new Error(
          'Your wallet needs enough tokens and reserve allowance for this deposit.',
        )
      if (action === 'withdraw' && n > BigInt(v.withdrawable_units ?? '0'))
        throw new Error('Enter an amount within your unfrozen principal.')
      const calldata = reserveCall(action, data.contract, n)
      // Persist intent before opening the wallet. A missing hash after a network
      // failure requires the user to inspect wallet activity before any retry.
      const entry: Pending = { action, started: Date.now() }
      remember(entry)
      const hash = await wallet.send(me.user.wallet, {
        action: `reserve_${action}`,
        amount_units: n.toString(),
        to: action === 'approve' ? data.token : data.contract,
        data: calldata,
        title: actions[action],
        description:
          action === 'withdraw'
            ? `Withdraw ${units(n.toString())} ${symbol} to your wallet.`
            : `${actions[action]}: ${units(n.toString())} ${symbol}.`,
      })
      if (!/^0x[0-9a-fA-F]{64}$/.test(hash))
        throw new Error(
          'No transaction hash returned. Check wallet activity before continuing.',
        )
      if (
        wallet.passkey?.(me.user.wallet) &&
        wallet.sponsored(me.user.wallet)
      ) {
        remember(null)
        if (action !== 'approve') setDialog(null)
        sync()
        return
      }
      remember({ ...entry, hash })
      setMessage(`${actions[action]} submitted.`)
    } catch (e) {
      const failure = e as Error & {
        code?: number | string
        operationId?: string
        beforeSubmission?: boolean
        terminal?: boolean
        notified?: boolean
      }
      if (
        failure.code === 4001 ||
        failure.beforeSubmission ||
        failure.terminal ||
        failure.code === 'insufficient_wallet_balance'
      )
        remember(null)
      else if (failure.operationId)
        remember({
          action,
          operationId: failure.operationId,
          started: Date.now(),
        })
      if (live.current && !failure.notified) setError((e as Error).message)
    } finally {
      transactionActive.current = false
      if (live.current) setWorking(false)
    }
  }
  const feedback = (
    <>
      {error && <Notice error>{error}</Notice>}
      {message && <Notice>{message}</Notice>}
      {pending && (
        <Notice>
          {pending.hash ? (
            <>
              <Clock3 size={14} /> {actions[pending.action]} is confirming.{' '}
              <a
                target="_blank"
                rel="noreferrer"
                href={`${explorer()}/tx/${pending.hash}`}
              >
                View transaction ↗
              </a>
            </>
          ) : working ? (
            <>
              <Clock3 size={14} /> Waiting for confirmation…
            </>
          ) : (
            <>
              Check your wallet for the pending action. If the connection was
              interrupted, review wallet activity before continuing.
              {!working && !pending.operationId && (
                <button
                  className="c-button c-quiet"
                  onClick={() => {
                    remember(null)
                    setError('')
                    sync()
                  }}
                >
                  I’ve checked wallet activity
                </button>
              )}
            </>
          )}
        </Notice>
      )}
    </>
  )
  return (
    <>
      <PageTitle
        title="Tier & reserve"
        description="Put your reserve to work. Unlock lower prices and more capacity."
        action={
          <button
            className="c-button"
            disabled={working || status.loading}
            onClick={() => {
              sync()
            }}
          >
            <RefreshCw size={15} /> Refresh
          </button>
        }
      />
      <State
        loading={status.loading && !data}
        error={status.error || tiers.error}
      />
      {!dialog && feedback}
      {data && !data.enabled && (
        <Notice>Reserve is disabled for this workspace.</Notice>
      )}
      <section className="c-panel c-reserve-hero">
        <div>
          <span className="c-eyebrow">
            <Layers size={16} /> YOUR RESERVE
          </span>
          <div className="c-reserve-principal">
            {snapshot ? units(snapshot.principal_units) : '—'}{' '}
            <span>{symbol}</span>
          </div>
          {snapshot && (
            <p>
              <strong>
                {units(snapshot.withdrawable_units ?? '0')} {symbol} available
              </strong>{' '}
              · {units(snapshot.frozen_units ?? '0')} {symbol} frozen for
              reported debt.
            </p>
          )}
          <p>
            Principal remains yours. API usage is paid separately from your
            wallet.
          </p>
          {snapshot && BigInt(snapshot.frozen_units ?? '0') > 0n && (
            <p>
              <a href="#billing">
                Pay outstanding bills to release frozen reserve ↗
              </a>
            </p>
          )}
          <div className="c-payment-actions">
            <button
              className="c-button c-primary"
              disabled={!snapshot || busy || !!status.error || snapshot.paused}
              onClick={() => {
                setAmount('50')
                setError('')
                setMessage('')
                setDialog('deposit')
              }}
            >
              <ArrowDownLeft size={16} /> Add reserve
            </button>
            <button
              className="c-button"
              disabled={
                !snapshot ||
                BigInt(snapshot?.withdrawable_units ?? '0') === 0n ||
                busy ||
                !!status.error
              }
              onClick={() => {
                setAmount(
                  units(snapshot!.withdrawable_units ?? '0').replaceAll(
                    ',',
                    '',
                  ),
                )
                setError('')
                setMessage('')
                setDialog('withdraw')
              }}
            >
              <ArrowUpRight size={16} /> Withdraw
            </button>
          </div>
        </div>
        <div className="c-reserve-benefits">
          <Badge tone="lime">
            {tierName(currentTier.id)}
            {data?.tier_override ? ' · manual' : ' tier'}
          </Badge>
          <strong>
            {currentTier.discount_bps / 100}% <span>off API usage</span>
          </strong>
          <div>
            <span>
              <b>{currentTier.rpm.toLocaleString()}</b> requests / min
            </span>
            <span>
              <b>{currentTier.concurrency}</b> concurrent
            </span>
          </div>
          {next && snapshot && !data?.tier_override && (
            <p>
              Add {money(Number(next.min_principal) - Number(principal) / 1e6)}{' '}
              {symbol} to reach {tierName(next.id)}.
            </p>
          )}
        </div>
      </section>
      {snapshot?.paused && (
        <Notice>
          New deposits are temporarily paused. Withdrawal management remains
          available.
        </Notice>
      )}
      <div className="c-section-title">
        <h2>A tier for every stage</h2>
        <span>Based on confirmed principal</span>
      </div>
      <div className="c-tier-grid">
        {tiers.data?.data.map((t) => (
          <section
            key={t.id}
            className={`c-panel c-tier-card ${t.id === currentTier.id ? 'is-current' : ''}`}
          >
            <div className="c-panel-title">
              <h3>{tierName(t.id)}</h3>
              {t.id === currentTier.id && <Badge tone="lime">Current</Badge>}
            </div>
            <strong>
              {t.discount_bps / 100}% <span>discount</span>
            </strong>
            <p>
              {Number(t.min_principal) === 0
                ? 'No reserve required'
                : `${money(t.min_principal ?? 0, 0)} ${symbol} reserve`}
            </p>
            <div>
              <span>{t.rpm.toLocaleString()} requests / min</span>
              <span>{t.concurrency} concurrent calls</span>
              <span>
                Settle at {money(t.settlement_threshold)} {symbol} or 5s
              </span>
            </div>
          </section>
        ))}
      </div>
      {data?.enabled && (
        <section className="c-panel c-reserve-notes">
          <div>
            <ShieldCheck size={19} />
            <p>
              {data.testnet_mock
                ? `${data.network} · ${symbol} test tokens. Reserve uses a simulated Aave pool. It does not generate real yield.`
                : `${data.network} · ${symbol} stays in the reserve contract.`}
            </p>
          </div>
          <details>
            <summary>How your reserve works</summary>
            <p>
              Your confirmed reserve determines your tier, API discount and
              request limits. Higher tiers offer larger discounts and more
              concurrent requests. Deposits and withdrawals update your tier
              automatically. Each API request keeps the discount applied when
              it began.
            </p>
            <p>
              You can withdraw available reserve funds at any time. If a bill
              cannot be collected, only the corresponding unpaid amount is
              frozen in your reserve; the rest remains available to withdraw.
              Pay the failed bill through Wallet &amp; billing to release the
              frozen amount.
            </p>
            {!!data.reports?.length && (
              <>
                <h3>Recent debt reports</h3>
                {data.reports.map((a) => (
                  <p key={a.tx_hash}>
                    <a
                      target="_blank"
                      rel="noreferrer"
                      href={`${explorer()}/tx/${a.tx_hash}`}
                    >
                      {short(a.tx_hash)} ↗
                    </a>{' '}
                    · {a.state} · {date(a.created_at)}
                  </p>
                ))}
              </>
            )}
          </details>
        </section>
      )}
      {dialog && snapshot && (
        <Modal
          title={
            dialog === 'deposit' ? 'Add to your reserve' : 'Withdraw reserve'
          }
          busy={working}
          onClose={() => setDialog(null)}
        >
          <form
            className="c-form"
            onSubmit={(e) => {
              e.preventDefault()
              void transaction(
                dialog === 'withdraw'
                  ? 'withdraw'
                  : needsAllowance
                    ? 'approve'
                    : 'deposit',
              )
            }}
          >
            <p>
              {dialog === 'deposit'
                ? 'Increase your principal to unlock a higher tier.'
                : 'Withdraw your available principal directly to this wallet. Reported debt stays frozen until payment or a manager release.'}
            </p>
            <label>
              Amount · {symbol}
              <input
                data-autofocus
                inputMode="decimal"
                autoComplete="off"
                maxLength={25}
                value={amount}
                disabled={busy}
                onChange={(e) => setAmount(e.target.value)}
              />
            </label>
            <span className="c-muted">
              {dialog === 'deposit'
                ? `Wallet balance: ${units(snapshot.balance_units)} ${symbol}`
                : `Available principal: ${units(snapshot.withdrawable_units ?? '0')} ${symbol}`}
            </span>
            {dialog === 'deposit' && (
              <p className="c-muted">
                {needsAllowance
                  ? 'First approve this amount for Reserve, then confirm your deposit.'
                  : 'Reserve allowance is ready. Confirm your deposit to continue.'}{' '}
                Keep enough in your wallet for API bills.
              </p>
            )}
            {feedback}
            <button
              className="c-button c-primary"
              disabled={busy || entered === 0n || !!status.error}
            >
              {busy
                ? 'Waiting for your transaction…'
                : dialog === 'withdraw'
                  ? 'Withdraw'
                  : needsAllowance
                    ? `Approve ${amount} ${symbol}`
                    : `Deposit ${amount} ${symbol}`}{' '}
              <ArrowUpRight size={15} />
            </button>
          </form>
        </Modal>
      )}
    </>
  )
}
