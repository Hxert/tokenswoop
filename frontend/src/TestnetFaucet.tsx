import { useEffect, useRef, useState } from 'react'
import { Droplets } from 'lucide-react'
import { api, APIError, date, useResource } from './api'
import { network, networkStorageKey } from './network'
import { Modal, Notice } from './ui'
import Turnstile from './Turnstile'

type Claim = {
  id: string
  wallet: string
  amount_units: string
  state: string
  hash: string
  error_code: string
  created_at: string
}
type Status = {
  enabled: boolean
  site_key: string
  amount_units: string
  cooldown_seconds: number
  next_claim_at: string | null
  claim: Claim | null
}
function savedClaimID(key: string) {
  try {
    const value = localStorage.getItem(key)
    return value && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(value)
      ? value
      : null
  } catch {
    return null
  }
}
export default function TestnetFaucet({
  enabled,
  account,
  onReceived,
}: {
  enabled: boolean
  account: string
  onReceived: () => void
}) {
  const status = useResource<Status>('/api/faucet', enabled, true)
  const [dialog, setDialog] = useState(false),
    [token, setToken] = useState(''),
    [error, setError] = useState(''),
    [verificationError, setVerificationError] = useState(''),
    [sending, setSending] = useState(false),
    [attempt, setAttempt] = useState(0),
    [claim, setClaim] = useState<Claim | null>(null),
    [result, setResult] = useState<Claim | null>(null),
    [now, setNow] = useState(Date.now())
  const storageKey = networkStorageKey(
    'tokenswoop:faucet:v1',
    account.toLowerCase(),
  )
  const [id, setID] = useState(
    () => savedClaimID(storageKey) || crypto.randomUUID(),
  )
  const active = useRef(false),
    notified = useRef(''),
    received = useRef(onReceived)
  received.current = onReceived
  useEffect(() => {
    const incoming = status.data?.claim
    if (incoming)
      setClaim((current) => {
        // A status read started before submission may arrive after the POST.
        if (
          current &&
          (Date.parse(current.created_at) > Date.parse(incoming.created_at) ||
            (current.id === incoming.id &&
              ['confirmed', 'failed'].includes(current.state)))
        )
          return current
        return incoming
      })
  }, [status.data])
  const pending =
    claim && ['queued', 'submitted', 'review'].includes(claim.state)
  const next = status.data?.next_claim_at
    ? Date.parse(status.data.next_claim_at)
    : 0
  const cooldown = next > now
  useEffect(() => {
    if (!enabled) return
    const timer = setInterval(
      () => {
        setNow(Date.now())
        status.reload()
      },
      pending ? 2500 : 15000,
    )
    return () => clearInterval(timer)
  }, [enabled, !!pending, status.reload])
  useEffect(() => {
    if (
      !claim ||
      !['confirmed', 'failed'].includes(claim.state) ||
      notified.current === claim.id
    )
      return
    // Only announce a claim initiated here or one still saved after a refresh.
    const saved = savedClaimID(storageKey)
    if (saved !== claim.id) return
    notified.current = claim.id
    try {
      localStorage.removeItem(storageKey)
    } catch {
      /* Status remains recoverable from the server. */
    }
    setResult(claim)
    setDialog(false)
    if (claim.state === 'confirmed') received.current()
    setID(crypto.randomUUID())
  }, [claim, storageKey])
  const submit = async () => {
    if (active.current || !token || !status.data?.enabled) return
    active.current = true
    setSending(true)
    setError('')
    try {
      localStorage.setItem(storageKey, id)
      const value = await api<Claim>('/api/faucet/claims', {
        method: 'POST',
        body: JSON.stringify({ id, turnstile_token: token }),
        signal: AbortSignal.timeout(15000),
      })
      setClaim(value)
      setDialog(false)
      status.reload()
    } catch (e) {
      // A lost HTTP response may still have queued the transfer. Read this ID;
      // retries use the same ID and a fresh Turnstile challenge.
      try {
        const saved = await api<Claim>(`/api/faucet/claims/${id}`, {
          signal: AbortSignal.timeout(8000),
        })
        setClaim(saved)
        setDialog(false)
        status.reload()
      } catch {
        setError(
          (e as Error).name === 'TimeoutError'
            ? 'Could not confirm the request. Try again to check the same claim.'
            : (e as Error).message,
        )
      }
      if (e instanceof APIError && e.code === 'faucet_cooldown') status.reload()
    } finally {
      active.current = false
      setSending(false)
      setToken('')
      setAttempt((v) => v + 1)
    }
  }
  if (!network().testnet) return null
  return (
    <section className="c-panel c-testnet-faucet">
      <div className="c-panel-title">
        <div>
          <h2>
            <Droplets size={20} /> Testnet tokens
          </h2>
          <p>Claim 10 kUSDC to test API payments and reserve.</p>
        </div>
        <button
          className="c-button c-primary"
          disabled={
            !enabled ||
            (status.loading && !status.data) ||
            !!pending ||
            cooldown ||
            sending ||
            !!status.error
          }
          onClick={() => {
            setError('')
            setVerificationError('')
            setToken('')
            setDialog(true)
          }}
        >
          {pending ? 'Claim processing…' : 'Get 10 kUSDC'}
        </button>
      </div>
      {!enabled ? (
        <p className="c-field-hint">The faucet is temporarily unavailable.</p>
      ) : (
        <p className="c-field-hint">
          {cooldown
            ? `Next claim: ${date(status.data!.next_claim_at!)}`
            : `One claim every ${(status.data?.cooldown_seconds ?? 86400) / 3600} hours per wallet.`}
        </p>
      )}
      {pending && (
        <Notice>
          {claim.state === 'review'
            ? 'Your claim needs a platform check. Its transaction has been saved.'
            : claim.error_code === 'faucet_empty'
              ? 'The faucet is being refilled. Your claim is saved.'
              : 'Sending 10 kUSDC to your wallet. The result will appear automatically.'}
          {claim.hash && (
            <>
              {' '}
              <a
                href={`${network().explorer}/tx/${claim.hash}`}
                target="_blank"
                rel="noreferrer"
              >
                View transaction ↗
              </a>
            </>
          )}
        </Notice>
      )}
      {status.error && enabled && (
        <Notice error>
          {status.error}
          <button className="c-button c-small" onClick={status.reload}>
            Retry
          </button>
        </Notice>
      )}
      {dialog && status.data && (
        <Modal
          title="Get 10 kUSDC"
          busy={sending}
          onClose={() => setDialog(false)}
        >
          <p>
            Complete verification to receive 10 kUSDC in your connected wallet.
            No gas or wallet approval is required.
          </p>
          <Turnstile
            siteKey={status.data.site_key}
            claimID={id}
            attempt={attempt}
            onToken={setToken}
            onError={setVerificationError}
          />
          {(error || verificationError) && (
            <Notice error>
              {error || verificationError}
              <button
                className="c-button c-small"
                disabled={sending}
                onClick={() => {
                  setError('')
                  setVerificationError('')
                  setAttempt((v) => v + 1)
                }}
              >
                Retry verification
              </button>
            </Notice>
          )}
          <div className="c-modal-actions">
            <button
              className="c-button"
              disabled={sending}
              onClick={() => setDialog(false)}
            >
              Cancel
            </button>
            <button
              className="c-button c-primary"
              disabled={sending || !token}
              onClick={() => void submit()}
            >
              {sending ? 'Submitting…' : 'Claim 10 kUSDC'}
            </button>
          </div>
        </Modal>
      )}
      {result && (
        <Modal
          title={
            result.state === 'confirmed'
              ? '10 kUSDC received'
              : 'Claim unsuccessful'
          }
          onClose={() => setResult(null)}
        >
          <Notice error={result.state !== 'confirmed'}>
            {result.state === 'confirmed'
              ? '10 kUSDC has arrived in your wallet.'
              : result.error_code === 'claim_expired'
                ? 'The faucet could not send your tokens in time. Please try again.'
                : 'The transfer failed. You can try claiming again.'}
          </Notice>
          <div className="c-modal-actions">
            {result.hash && (
              <a
                className="c-button"
                href={`${network().explorer}/tx/${result.hash}`}
                target="_blank"
                rel="noreferrer"
              >
                View transaction ↗
              </a>
            )}
            <button
              className="c-button c-primary"
              onClick={() => setResult(null)}
            >
              Done
            </button>
          </div>
        </Modal>
      )}
    </section>
  )
}
