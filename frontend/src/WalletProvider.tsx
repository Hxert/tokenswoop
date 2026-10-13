import {
  createContext,
  lazy,
  Suspense,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react'
import type { ReactNode } from 'react'
import { ArrowLeft, Fingerprint, Mail, Plus, Wallet } from 'lucide-react'
import { createPublicClient } from 'viem'
import { network } from './network'
import { chainRpcTransport } from './chainRpc'
import {
  api,
  APIError,
  loginWithWalletSignature,
  useResource,
  walletLogin,
} from './api'
import type { Config, Me } from './api'
import { CopyButton, CopyValue, Loading, Modal, Notice } from './ui'
import { sendExternal } from './walletTransport'
import { checkApprovalBalance } from './walletApproval'
import type { WalletOperation, WalletReceipt } from './walletTransport'
import {
  clearWalletLogin,
  readWalletLogin,
  setLoginIntent,
  setWalletLogin,
  walletLoginEvent,
  walletLoginKey,
} from './walletLoginState'
import type { LoginSource } from './walletLoginState'
import {
  clearMeraTransaction,
  passkeyAvailable,
  pendingMeraTransaction,
  resumeMeraTransaction,
  sendMeraTransaction,
  withMeraWallet,
} from './meraWallet'
import {
  sendSponsoredMera,
  resumeSponsoredMera,
  sponsoredMeraPending,
  reconcileSponsoredMera,
  restoreSponsoredMera,
  watchSponsoredMera,
} from './meraSponsorship'

const PrivyWalletProvider = lazy(() => import('./PrivyWalletProvider'))
const chain = createPublicClient({
  transport: chainRpcTransport(),
})
export async function walletReceipt(hash: string): Promise<WalletReceipt> {
  if (!/^0x[0-9a-fA-F]{64}$/.test(hash))
    throw new Error('Invalid transaction hash.')
  try {
    const result = await chain.getTransactionReceipt({
      hash: hash as `0x${string}`,
    })
    return { status: result.status === 'success' ? '0x1' : '0x0' }
  } catch (error) {
    if ((error as Error).name === 'TransactionReceiptNotFoundError') return null
    throw error
  }
}
export type WalletConnection = {
  enabled: boolean
  ready: boolean
  busy: boolean
  sessionVersion: number
  error: string
  login: () => Promise<void>
  logout: () => Promise<void>
  embedded: (account: string) => boolean
  sponsored: (account: string) => boolean
  passkey?: (account: string) => boolean
  backup?: (account: string) => void
  send: (account: string, operation: WalletOperation) => Promise<string>
  receipt: typeof walletReceipt
  pending?: (
    account: string,
  ) => { id: string; account: string; operation?: WalletOperation } | null
  resume?: (account: string) => Promise<string>
}
const WalletContext = createContext<WalletConnection | null>(null)
export const PrivyConnectionContext = createContext<WalletConnection | null>(
  null,
)
export function useWalletConnection() {
  const wallet = useContext(WalletContext)
  if (!wallet) throw new Error('Wallet provider is unavailable.')
  return wallet
}
const cancelled = () =>
  Object.assign(new Error('Wallet operation cancelled.'), { code: 4001 })

function UnifiedWalletProvider({
  children,
  mera,
}: {
  children: ReactNode
  mera?: Config['mera']
}) {
  const privy = useContext(PrivyConnectionContext)
  const [loginState, setLoginState] = useState(readWalletLogin)
  const [sessionVersion, setSessionVersion] = useState(0)
  const [loginStep, setLoginStep] = useState<'methods' | 'passkey' | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [theme, setTheme] = useState(
    document.documentElement.dataset.workspaceTheme || 'dark',
  )
  const [backupAccount, setBackupAccount] = useState('')
  const [phrase, setPhrase] = useState('')
  const [, setJournalVersion] = useState(0)
  const working = useRef(false)
  const [result, setResult] = useState<{
    title: string
    message: string
    hash?: string
    explorer: string
    error: boolean
  } | null>(null)
  const shownResults = useRef(new Set<string>())
  const reportResult = (
    operation: WalletOperation,
    hash?: string,
    failure?: Error & {
      terminal?: boolean
      review?: boolean
      operationId?: string
      hash?: string
      code?: string | number
    },
  ) => {
    if (
      failure &&
      !failure.terminal &&
      !failure.review &&
      failure.code !== 'insufficient_wallet_balance'
    )
      return
    const id = failure?.operationId || hash
    if (id && shownResults.current.has(id)) return
    if (id) shownResults.current.add(id)
    setResult({
      title: failure
        ? failure.review
          ? 'Transaction needs attention'
          : 'Transaction unsuccessful'
        : 'Transaction confirmed',
      message: failure?.message || `${operation.title} completed successfully.`,
      hash: hash || failure?.hash,
      explorer: network().explorer,
      error: !!failure,
    })
    if (failure) Object.assign(failure, { notified: true })
    window.dispatchEvent(new Event('tokenswoop:wallet-transaction'))
  }
  const completeSponsored = async (
    operation: WalletOperation,
    submit: () => Promise<string>,
  ) => {
    try {
      const hash = await submit()
      reportResult(operation, hash)
      return hash
    } catch (failure) {
      reportResult(operation, undefined, failure as Error)
      throw failure
    }
  }
  const [confirmation, setConfirmation] = useState<{
    account: string
    operation: WalletOperation
    resolve: (yes: boolean) => void
  } | null>(null)
  useEffect(() => {
    const changed = () => {
      setLoginIntent(false)
      setLoginState(readWalletLogin())
      setSessionVersion((v) => v + 1)
      setLoginStep(null)
      setBackupAccount('')
      setPhrase('')
      setError('')
      setResult(null)
    }
    const storage = (event: StorageEvent) => {
      if (event.key === walletLoginKey || event.key === null) changed()
    }
    const themeObserver = new MutationObserver(() =>
      setTheme(document.documentElement.dataset.workspaceTheme || 'dark'),
    )
    setTheme(document.documentElement.dataset.workspaceTheme || 'dark')
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-workspace-theme'],
    })
    window.addEventListener(walletLoginEvent, changed)
    window.addEventListener('storage', storage)
    return () => {
      themeObserver.disconnect()
      window.removeEventListener(walletLoginEvent, changed)
      window.removeEventListener('storage', storage)
    }
  }, [])
  useEffect(() => {
    if (!phrase) return
    const timer = setTimeout(() => setPhrase(''), 60000)
    const hide = () => {
      if (document.hidden) setPhrase('')
    }
    document.addEventListener('visibilitychange', hide)
    return () => {
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', hide)
    }
  }, [phrase])
  const source = (account: string) =>
    loginState?.wallet.toLowerCase() === account.toLowerCase()
      ? loginState.source
      : 'external'
  const guard = async (account: string) => {
    const current = await api<Me>('/api/me')
    if (current.user.wallet.toLowerCase() !== account.toLowerCase()) {
      setSessionVersion((v) => v + 1)
      throw new Error(
        'The signed-in wallet has changed. Review the current account before continuing.',
      )
    }
  }
  const run = async <T,>(action: () => Promise<T>) => {
    if (working.current || privy?.busy)
      throw new Error('Finish the current wallet operation first.')
    working.current = true
    setBusy(true)
    setError('')
    try {
      return await action()
    } finally {
      working.current = false
      setBusy(false)
      setJournalVersion((v) => v + 1)
    }
  }
  useEffect(() => {
    if (!mera?.gas_sponsorship || loginState?.source !== 'mera') return
    const controller = new AbortController()
    const account = loginState.wallet
    const restore = async () => {
      if (working.current || controller.signal.aborted) return
      working.current = true
      let operation: WalletOperation | undefined
      try {
        const saved = await restoreSponsoredMera(account, mera)
        if (controller.signal.aborted) return
        setJournalVersion((v) => v + 1)
        operation = sponsoredMeraPending(account)?.operation
        if (!saved || !operation || saved.state === 'prepared') return
        setBusy(true)
        const hash = await watchSponsoredMera(account, controller.signal)
        if (hash && !controller.signal.aborted) reportResult(operation, hash)
      } catch (failure) {
        if (operation && !controller.signal.aborted)
          reportResult(operation, undefined, failure as Error)
        // A read error retains the original ID; recovery never asks for a passkey.
      } finally {
        working.current = false
        setBusy(false)
        setJournalVersion((v) => v + 1)
      }
    }
    void restore()
    const interval = setInterval(() => void restore(), 10000)
    return () => {
      controller.abort()
      clearInterval(interval)
    }
  }, [
    loginState?.wallet,
    loginState?.source,
    mera?.implementation,
    mera?.gas_sponsorship,
  ])
  const selectLogin = async (method: LoginSource, create = false) => {
    setError('')
    try {
      if (method === 'privy') {
        if (!privy?.ready)
          throw new Error(
            'Email sign-in is initializing. Please try again in a moment.',
          )
        setLoginStep(null)
        await privy.login()
      } else {
        await run(async () => {
          setLoginIntent(false)
          const user =
            method === 'external'
              ? await walletLogin()
              : await withMeraWallet({ create }, (account) =>
                  loginWithWalletSignature(
                    account.address,
                    (message) => account.signMessage({ message }),
                    'mera',
                  ),
                )
          setWalletLogin(method, user.wallet)
        })
      }
    } catch (e) {
      setError((e as Error).message)
    }
  }
  const closeConfirmation = (yes: boolean) => {
    confirmation?.resolve(yes)
    setConfirmation(null)
  }
  const send = (account: string, operation: WalletOperation) =>
    run(async () => {
      await guard(account)
      await checkApprovalBalance(account, operation)
      if (source(account) === 'mera') {
        const yes = await new Promise<boolean>((resolve) =>
          setConfirmation({ account, operation, resolve }),
        )
        if (!yes) throw cancelled()
        return mera?.gas_sponsorship
          ? completeSponsored(operation, () =>
              sendSponsoredMera(account, operation, mera, () => guard(account)),
            )
          : sendMeraTransaction(account, operation, () => guard(account))
      }
      if (source(account) === 'privy' && privy)
        return privy.send(account, operation)
      if (!window.ethereum)
        throw new Error('Connect your wallet extension to continue.')
      return sendExternal(window.ethereum, account, operation)
    })
  return (
    <WalletContext.Provider
      value={{
        enabled: true,
        ready: true,
        busy: busy || !!privy?.busy,
        sessionVersion,
        error: error || privy?.error || '',
        login: async () => {
          if (working.current || privy?.busy)
            throw new Error('Finish the current wallet operation first.')
          setError('')
          setLoginStep('methods')
        },
        logout: () =>
          run(async () => {
            setLoginIntent(false)
            if (privy) await privy.logout()
            else
              await api('/api/auth/logout', { method: 'POST' }).catch((e) => {
                if (!(e instanceof APIError) || e.status !== 401) throw e
              })
            clearWalletLogin()
          }),
        embedded: (account) =>
          source(account) === 'privy' && !!privy?.embedded(account),
        sponsored: (account) =>
          source(account) === 'mera'
            ? !!mera?.gas_sponsorship
            : source(account) === 'privy' && !!privy?.sponsored(account),
        passkey: (account) => source(account) === 'mera',
        backup: (account) => {
          if (!working.current) {
            setPhrase('')
            setError('')
            setBackupAccount(account)
          }
        },
        send,
        receipt: async (hash) => {
          const receipt = await walletReceipt(hash)
          if (receipt && loginState?.source === 'mera') {
            clearMeraTransaction(loginState.wallet, hash)
            await reconcileSponsoredMera(loginState.wallet)
            setJournalVersion((v) => v + 1)
          }
          return receipt
        },
        pending: (account) => {
          if (source(account) === 'mera') {
            try {
              return (
                sponsoredMeraPending(account) || pendingMeraTransaction(account)
              )
            } catch {
              return { id: 'unreadable', account }
            }
          }
          return source(account) === 'privy'
            ? privy?.pending?.(account) || null
            : null
        },
        resume: (account) =>
          run(async () => {
            await guard(account)
            if (source(account) !== 'mera') {
              if (source(account) !== 'privy' || !privy?.resume)
                throw new Error(
                  'Sign in with the original wallet to resume this transaction.',
                )
              return privy.resume(account)
            }
            const pending = sponsoredMeraPending(account)
            if (pending) {
              const yes = await new Promise<boolean>((resolve) =>
                setConfirmation({
                  account,
                  operation: pending.operation,
                  resolve,
                }),
              )
              if (!yes) throw cancelled()
            }
            const hash =
              pending && mera
                ? await completeSponsored(pending.operation, () =>
                    resumeSponsoredMera(account, mera, () => guard(account)),
                  )
                : await resumeMeraTransaction(account)
            const receipt = await walletReceipt(hash)
            if (receipt) clearMeraTransaction(account, hash)
            window.dispatchEvent(new Event('tokenswoop:wallet-transaction'))
            return hash
          }),
      }}
    >
      {children}
      <div className="console c-wallet-layer" data-theme={theme}>
        {loginStep === 'methods' && (
          <Modal
            title="Sign in to TokenSwoop"
            busy={busy}
            onClose={() => setLoginStep(null)}
          >
            <p>Choose your wallet. Each wallet has its own workspace.</p>
            <div className="c-login-methods">
              <button
                className="c-login-choice"
                disabled={busy || !passkeyAvailable()}
                onClick={() => {
                  setError('')
                  setLoginStep('passkey')
                }}
              >
                <Fingerprint size={24} />
                <span>
                  <strong>Passkey</strong>
                  <small>Your Mera wallet, unlocked with your device.</small>
                </span>
              </button>
              <button
                className="c-login-choice"
                disabled={busy || !privy?.ready}
                onClick={() => void selectLogin('privy')}
              >
                <Mail size={24} />
                <span>
                  <strong>Email or Google</strong>
                  <small>
                    {privy
                      ? 'A Privy embedded wallet for your account.'
                      : 'Email and Google sign-in is unavailable in this workspace.'}
                  </small>
                </span>
              </button>
              <button
                className="c-login-choice"
                disabled={busy}
                onClick={() => void selectLogin('external')}
              >
                <Wallet size={24} />
                <span>
                  <strong>Connect a wallet</strong>
                  <small>Use your existing Ethereum wallet extension.</small>
                </span>
              </button>
            </div>
            {!passkeyAvailable() && (
              <Notice>
                Passkeys require a supported browser on HTTPS or localhost.
              </Notice>
            )}
            {busy && <Notice>Complete the request on your device…</Notice>}
            {error && <Notice error>{error}</Notice>}
          </Modal>
        )}
        {loginStep === 'passkey' && (
          <Modal
            title="Continue with passkey"
            busy={busy}
            onClose={() => setLoginStep(null)}
          >
            <p>Log in to your wallet or create one to get started.</p>
            <div className="c-login-methods">
              <button
                className="c-login-choice"
                disabled={busy}
                onClick={() => void selectLogin('mera')}
              >
                <Fingerprint size={24} />
                <span>
                  <strong>Log in with passkey</strong>
                  <small>Use a passkey you already saved.</small>
                </span>
              </button>
              <button
                className="c-login-choice"
                disabled={busy}
                onClick={() => void selectLogin('mera', true)}
              >
                <Plus size={24} />
                <span>
                  <strong>Create a passkey</strong>
                  <small>Set up a new passkey and wallet.</small>
                </span>
              </button>
            </div>
            <button
              className="c-button c-quiet"
              disabled={busy}
              onClick={() => {
                setError('')
                setLoginStep('methods')
              }}
            >
              <ArrowLeft size={16} /> Back
            </button>
            {busy && <Notice>Complete the request on your device…</Notice>}
            {error && <Notice error>{error}</Notice>}
          </Modal>
        )}
        {confirmation && (
          <Modal
            title={confirmation.operation.title}
            onClose={() => closeConfirmation(false)}
          >
            <p>{confirmation.operation.description}</p>
            <dl className="c-wallet-confirm-details">
              <div>
                <dt>Wallet</dt>
                <dd>
                  <CopyValue
                    value={confirmation.account}
                    label="Copy wallet address"
                  />
                </dd>
              </div>
              <div>
                <dt>Network</dt>
                <dd>{network().name}</dd>
              </div>
              <div>
                <dt>Contract</dt>
                <dd>
                  <CopyValue
                    value={confirmation.operation.to}
                    label="Copy contract address"
                  />
                </dd>
              </div>
              <div>
                <dt>Network fee</dt>
                <dd>
                  {mera?.gas_sponsorship
                    ? 'Sponsored by TokenSwoop'
                    : 'Paid in MON from this wallet'}
                </dd>
              </div>
            </dl>
            {mera?.gas_sponsorship && (
              <p className="c-field-hint">
                First use enables smart account execution for this wallet. Each
                new operation requires your passkey confirmation.
              </p>
            )}
            <div className="c-modal-actions">
              <button
                className="c-button"
                onClick={() => closeConfirmation(false)}
              >
                Cancel
              </button>
              <button
                className="c-button c-primary"
                onClick={() => closeConfirmation(true)}
              >
                Confirm with passkey
              </button>
            </div>
          </Modal>
        )}
        {backupAccount && (
          <Modal
            title="Back up passkey wallet"
            busy={busy}
            onClose={() => {
              setPhrase('')
              setBackupAccount('')
            }}
          >
            <p>
              Your recovery phrase restores this wallet if your passkey or
              access to this site is lost. Keep it private and offline.
            </p>
            <CopyValue value={backupAccount} label="Copy wallet address" />
            {phrase ? (
              <>
                <div className="c-recovery-heading">
                  <span>Recovery phrase</span>
                  <CopyButton value={phrase} label="Copy recovery phrase" />
                </div>
                <ol className="c-recovery-words">
                  {phrase.split(' ').map((word, i) => (
                    <li key={i}>
                      <span>{i + 1}</span>
                      {word}
                    </li>
                  ))}
                </ol>
                <p className="c-field-hint">
                  Hidden after one minute or when you leave this tab.
                </p>
                <button className="c-button" onClick={() => setPhrase('')}>
                  Hide phrase
                </button>
              </>
            ) : (
              <button
                className="c-button c-primary"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await guard(backupAccount)
                    await withMeraWallet(
                      { expectedAddress: backupAccount },
                      async (_account, words) => {
                        setPhrase(words)
                      },
                    )
                  }).catch((e) => setError((e as Error).message))
                }
              >
                {busy ? 'Confirm on your device…' : 'Reveal recovery phrase'}
              </button>
            )}
            {error && <Notice error>{error}</Notice>}
          </Modal>
        )}
        {result && (
          <Modal title={result.title} onClose={() => setResult(null)}>
            <Notice error={result.error}>{result.message}</Notice>
            <div className="c-modal-actions">
              {result.hash && (
                <a
                  className="c-button"
                  href={`${result.explorer}/tx/${result.hash}`}
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
      </div>
    </WalletContext.Provider>
  )
}

export default function WalletProvider({ children }: { children: ReactNode }) {
  const config = useResource<Config>('/api/config')
  if (config.loading)
    return (
      <div className="c-wallet-loading">
        <Loading />
      </div>
    )
  if (config.error || !config.data)
    return (
      <div className="c-wallet-loading">
        <Notice error>
          {config.error || 'Wallet configuration is unavailable.'}
          <button className="c-button" onClick={config.reload}>
            Retry
          </button>
        </Notice>
      </div>
    )
  const workspace = (
    <UnifiedWalletProvider mera={config.data.mera}>
      {children}
    </UnifiedWalletProvider>
  )
  if (!config.data.privy?.enabled) return workspace
  return (
    <Suspense
      fallback={
        <div className="c-wallet-loading">
          <Loading />
        </div>
      }
    >
      <PrivyWalletProvider config={config.data.privy}>
        {workspace}
      </PrivyWalletProvider>
    </Suspense>
  )
}
