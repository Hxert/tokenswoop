import { RefreshCw } from 'lucide-react'
import { date, money, short, useResource } from './api'
import type { ReserveSnapshot } from './Reserve'
import { formatReserveUnits as units } from './reserveWallet'
import { Badge, CopyButton, Empty, Notice, State } from './ui'
import { network } from './network'
export default function AdminReserve() {
  const reserve = useResource<{
    enabled: boolean
    data: {
      user_id: string
      wallet: string
      tier: string
      tier_override: boolean
      principal_units: string
      unpaid: string
      snapshot: ReserveSnapshot
      checked_at: string
      in_flight: number
      usage_unknown: number
      open_bills: number
    }[]
  }>('/api/admin/reserve')
  return (
    <>
      <div className="c-section-actions">
        <p>Confirmed principal, frozen debt and available withdrawals.</p>
        <button className="c-button" onClick={reserve.reload}>
          <RefreshCw size={15} /> Refresh
        </button>
      </div>
      <State loading={reserve.loading} error={reserve.error} />
      {reserve.data && !reserve.data.enabled && (
        <Notice>Reserve synchronization is disabled.</Notice>
      )}
      <section className="c-panel">
        <div className="c-table-wrap">
          <table>
            <thead>
              <tr>
                <th>Wallet</th>
                <th>Principal · {network().symbol}</th>
                <th>Tier</th>
                <th>Withdrawal</th>
                <th>Clearance</th>
                <th>Last checked</th>
              </tr>
            </thead>
            <tbody>
              {reserve.data?.data.map((r) => (
                <tr key={r.user_id}>
                  <td>
                    <span className="c-bill-id">
                      <code title={r.wallet}>{short(r.wallet)}</code>
                      <CopyButton
                        value={r.wallet}
                        label="Copy wallet address"
                      />
                    </span>
                  </td>
                  <td>{units(r.principal_units)}</td>
                  <td>
                    {r.tier}
                    <small>
                      {r.tier_override ? 'Manual override' : 'Automatic'}
                    </small>
                  </td>
                  <td>
                    {r.snapshot.withdrawable_units !== undefined ? (
                      <>
                        <Badge>
                          {units(r.snapshot.withdrawable_units)} available
                        </Badge>
                        <small>
                          {units(r.snapshot.frozen_units ?? '0')} frozen
                        </small>
                      </>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td>
                    {r.in_flight} active · {r.open_bills} open bills
                    <small>
                      {money(r.unpaid, 12)} unpaid · {r.usage_unknown} need
                      review
                    </small>
                    {r.usage_unknown > 0 && (
                      <a href="#admin?section=health">Review usage ↗</a>
                    )}
                  </td>
                  <td>{date(r.checked_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {reserve.data?.data.length === 0 && (
          <Empty title="No synchronized reserves yet" />
        )}
      </section>
      <Notice>
        Users can withdraw unfrozen principal directly. The management wallet
        can release freezes and pause deposits onchain.
      </Notice>
    </>
  )
}
