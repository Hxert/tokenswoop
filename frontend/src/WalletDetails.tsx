import { ArrowRightLeft, ArrowUpRight, LogOut, RefreshCw } from 'lucide-react'
import { formatUnits } from 'viem'
import { useResource } from './api'
import type { Me } from './api'
import type { BillingStatus } from './Billing'
import { network } from './network'
import { Badge, CopyValue, Modal, Notice } from './ui'
import { useWalletConnection } from './WalletProvider'

export default function WalletDetails({
  me,
  busy,
  onClose,
  onSwitch,
  onSignOut,
}: {
  me: Me
  busy: boolean
  onClose: () => void
  onSwitch: () => void
  onSignOut: () => void
}) {
  const wallet = useWalletConnection()
  const status = useResource<BillingStatus>('/api/billing')
  const chain = network()
  const data = status.data
  const snapshot = data?.chain_error ? undefined : data?.wallet
  const connection = wallet.passkey?.(me.user.wallet)
    ? 'Passkey · Mera'
    : wallet.embedded(me.user.wallet)
      ? 'Embedded wallet · Privy'
      : 'External wallet'
  const amount = (value?: string) =>
    value === undefined ? '—' : formatUnits(BigInt(value), data?.decimals ?? 6)

  return (
    <Modal title="Wallet details" onClose={onClose} busy={busy}>
      <div className="c-wallet-details">
        <div className="c-wallet-details-address">
          <span>Wallet address</span>
          <CopyValue value={me.user.wallet} label="Copy wallet address" />
          <a
            href={`${chain.explorer}/address/${me.user.wallet}`}
            target="_blank"
            rel="noreferrer"
          >
            View on explorer <ArrowUpRight size={15} aria-hidden="true" />
          </a>
        </div>
        <dl className="c-wallet-details-info">
          <div>
            <dt>Network</dt>
            <dd>{chain.name}</dd>
          </div>
          <div>
            <dt>Wallet type</dt>
            <dd>{connection}</dd>
          </div>
          <div>
            <dt>Tier</dt>
            <dd>
              <Badge>{me.tier.id} tier</Badge>
            </dd>
          </div>
        </dl>
        <div className="c-wallet-details-balances" aria-busy={status.loading}>
          <div className="c-wallet-details-balance-heading">
            <h3>Wallet balances</h3>
            <button
              type="button"
              className="c-icon"
              onClick={status.reload}
              disabled={status.loading || busy}
              aria-label="Refresh wallet balances"
              title="Refresh wallet balances"
            >
              <RefreshCw size={16} className={status.loading ? 'c-spin' : ''} />
            </button>
          </div>
          {status.loading ? (
            <p className="c-muted" role="status">
              Loading balances…
            </p>
          ) : status.error || data?.chain_error ? (
            <Notice error>{status.error || data?.chain_error}</Notice>
          ) : data && !data.enabled ? (
            <p className="c-muted">
              Wallet payments are unavailable on this server.
            </p>
          ) : (
            <dl className="c-wallet-details-info">
              <div>
                <dt>Wallet balance</dt>
                <dd>
                  {amount(snapshot?.balance_units)} {chain.symbol}
                </dd>
              </div>
              <div>
                <dt>Remaining allowance</dt>
                <dd>
                  {amount(snapshot?.allowance_units)} {chain.symbol}
                </dd>
              </div>
            </dl>
          )}
        </div>
        <div className="c-wallet-details-actions">
          <a className="c-button c-primary" href="#billing" onClick={onClose}>
            Wallet &amp; billing <ArrowUpRight size={16} aria-hidden="true" />
          </a>
          <button
            type="button"
            className="c-button"
            disabled={busy}
            onClick={onSwitch}
          >
            <ArrowRightLeft size={16} aria-hidden="true" /> Switch wallet
          </button>
          <button
            type="button"
            className="c-button c-quiet"
            disabled={busy}
            onClick={onSignOut}
          >
            <LogOut size={16} aria-hidden="true" /> Sign out
          </button>
        </div>
      </div>
    </Modal>
  )
}
