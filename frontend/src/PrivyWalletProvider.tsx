import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  PrivyProvider,
  useAuthorizationSignature,
  useCreateWallet,
  useLogin,
  usePrivy,
  useSendTransaction,
  useWallets,
} from '@privy-io/react-auth'
import type { User as PrivyUser } from '@privy-io/react-auth'
import { network, networkStorageKey } from './network'
import { api, APIError, short } from './api'
import type { Config, User as WorkspaceUser } from './api'
import { Modal, CopyValue } from './ui'
import { PrivyConnectionContext, walletReceipt } from './WalletProvider'
import {
  hasPrivyLoginIntent,
  setLoginIntent,
  setWalletLogin,
} from './walletLoginState'
import { checkAuthorization, sendExternal } from './walletTransport'
import type { SponsoredAuthorization, WalletOperation } from './walletTransport'

type Settings = NonNullable<Config['privy']>
type SavedOperation = {
  id: string
  account: string
  operation: WalletOperation
}
type Prepared = {
  id: string
  state: string
  hash?: string
  authorization?: SponsoredAuthorization
}
const journalKey = (appID: string, account: string) =>
  networkStorageKey('tokenswoop:wallet-operation', appID, account.toLowerCase())
function savedOperation(appID: string, account: string): SavedOperation | null {
  try {
    const value = JSON.parse(
      localStorage.getItem(journalKey(appID, account)) || 'null',
    )
    return value &&
      typeof value.id === 'string' &&
      value.account?.toLowerCase() === account.toLowerCase() &&
      value.operation?.action
      ? value
      : null
  } catch {
    return null
  }
}
const cancelled = () =>
  Object.assign(new Error('Wallet transaction cancelled.'), { code: 4001 })

export default function PrivyWalletProvider({
  children,
  config,
}: {
  children: ReactNode
  config: Settings
}) {
  const [theme, setTheme] = useState<'dark' | 'light'>(() =>
    document.documentElement.dataset.workspaceTheme === 'light'
      ? 'light'
      : 'dark',
  )
  useEffect(() => {
    setTheme(
      document.documentElement.dataset.workspaceTheme === 'light'
        ? 'light'
        : 'dark',
    )
    const observer = new MutationObserver(() =>
      setTheme(
        document.documentElement.dataset.workspaceTheme === 'light'
          ? 'light'
          : 'dark',
      ),
    )
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-workspace-theme'],
    })
    return () => observer.disconnect()
  }, [])
  return (
    <PrivyProvider
      appId={config.app_id}
      config={{
        loginMethods: ['email', 'google'],
        appearance: {
          theme,
          accentColor: theme === 'dark' ? '#b4ef63' : '#3c653c',
          walletChainType: 'ethereum-only',
        },
        supportedChains: [network().chain],
        defaultChain: network().chain,
        // Create explicitly in the login exchange so the backend never races automatic creation.
        embeddedWallets: {
          ethereum: { createOnLogin: 'off' },
          showWalletUIs: true,
        },
      }}
    >
      <ConnectedWalletProvider config={config}>
        {children}
      </ConnectedWalletProvider>
    </PrivyProvider>
  )
}

function ConnectedWalletProvider({
  children,
  config,
}: {
  children: ReactNode
  config: Settings
}) {
  const privy = usePrivy()
  const connected = useWallets()
  const { createWallet } = useCreateWallet()
  const { sendTransaction } = useSendTransaction()
  const { generateAuthorizationSignature } = useAuthorizationSignature()
  const [sessionVersion, setSessionVersion] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [, setJournalVersion] = useState(0)
  const exchange = useRef<Promise<void> | null>(null)
  const sending = useRef(false)
  const loggingOut = useRef(false)
  const bootstrap = useRef(false)
  const [confirmation, setConfirmation] = useState<{
    account: string
    operation: WalletOperation
    resolve: (yes: boolean) => void
  } | null>(null)
  const [walletSelection, setWalletSelection] = useState<{
    choices: { address: string; embedded: boolean }[]
    resolve: (address: string) => void
  } | null>(null)
  const chooseWallet = (choices: { address: string; embedded: boolean }[]) =>
    new Promise<string>((resolve) => setWalletSelection({ choices, resolve }))
  const closeSelection = (address: string) => {
    walletSelection?.resolve(address)
    setWalletSelection(null)
  }
  const walletFor = (account: string) =>
    connected.wallets.find(
      (w) => w.address.toLowerCase() === account.toLowerCase(),
    )
  const embedded = (account: string) =>
    walletFor(account)?.walletClientType === 'privy'
  const authenticate = (user: PrivyUser | null, hint = '') => {
    if (exchange.current) return exchange.current
    setBusy(true)
    setError('')
    exchange.current = (async () => {
      if (!user) throw new Error('Sign in to continue.')
      const choices = user.linkedAccounts
        .flatMap((a) =>
          a.type === 'wallet' &&
          a.chainType === 'ethereum' &&
          /^0x[0-9a-fA-F]{40}$/.test(a.address)
            ? [{ address: a.address, embedded: a.walletClientType === 'privy' }]
            : [],
        )
        .filter(
          (a, i, all) =>
            all.findIndex(
              (b) => b.address.toLowerCase() === a.address.toLowerCase(),
            ) === i,
        )
      if (!choices.length) {
        const created = await createWallet()
        hint = created.address
      } else if (!hint) {
        hint =
          choices.length === 1
            ? choices[0].address
            : await chooseWallet(choices)
        if (!hint) throw cancelled()
      }
      if (!hasPrivyLoginIntent() || loggingOut.current) throw cancelled()
      const token = await privy.getAccessToken()
      if (!token) throw new Error('Sign in again to verify your account.')
      // Fetch verified linked wallets server-side. No client-supplied email, role or address is trusted.
      for (let attempt = 0; ; attempt++) {
        try {
          const account = await api<WorkspaceUser>('/api/auth/privy', {
            method: 'POST',
            body: JSON.stringify({
              access_token: token,
              wallet: hint || undefined,
            }),
          })
          setWalletLogin('privy', account.wallet)
          break
        } catch (e) {
          if (
            !(e instanceof APIError) ||
            e.code !== 'wallet_not_ready' ||
            attempt >= 2
          )
            throw e
          await new Promise((resolve) => setTimeout(resolve, 1000))
        }
      }
      setSessionVersion((v) => v + 1)
    })()
      .catch((e) => {
        setLoginIntent(false)
        setError((e as Error).message)
        throw e
      })
      .finally(() => {
        exchange.current = null
        setBusy(false)
      })
    return exchange.current
  }
  const { login: openLogin } = useLogin({
    onComplete: ({ user, loginAccount }) => {
      if (!hasPrivyLoginIntent() || loggingOut.current) return
      bootstrap.current = true
      const hint = loginAccount?.type === 'wallet' ? loginAccount.address : ''
      void authenticate(user, hint).catch(() => {})
    },
    onError: () => {
      setLoginIntent(false)
      setBusy(false)
      setError('Sign-in was not completed. Please try again.')
    },
  })
  // Only an explicit Privy login may resume after OAuth. An idle SDK session never selects a workspace.
  useEffect(() => {
    if (
      !privy.ready ||
      !privy.authenticated ||
      !hasPrivyLoginIntent() ||
      bootstrap.current ||
      loggingOut.current
    )
      return
    bootstrap.current = true
    void authenticate(privy.user).catch(() => {})
  }, [privy.ready, privy.authenticated])
  const confirm = (account: string, operation: WalletOperation) =>
    new Promise<boolean>((resolve) =>
      setConfirmation({ account, operation, resolve }),
    )
  const closeConfirmation = (yes: boolean) => {
    confirmation?.resolve(yes)
    setConfirmation(null)
  }
  const journal = (account: string, value: SavedOperation | null) => {
    if (value)
      localStorage.setItem(
        journalKey(config.app_id, account),
        JSON.stringify(value),
      )
    else localStorage.removeItem(journalKey(config.app_id, account))
    setJournalVersion((v) => v + 1)
  }
  const send = async (
    account: string,
    operation: WalletOperation,
  ): Promise<string> => {
    if (sending.current)
      throw new Error('Finish the current wallet operation first.')
    if (!privy.ready || !connected.ready)
      throw new Error('Your wallet is still loading. Try again shortly.')
    sending.current = true
    let operationID = ''
    let attemptedSubmit = false
    try {
      const selected = walletFor(account)
      if (!selected) {
        if (!window.ethereum)
          throw new Error('Sign in again to reconnect your payment wallet.')
        return await sendExternal(window.ethereum, account, operation)
      }
      if (selected.walletClientType !== 'privy')
        return await sendExternal(
          await selected.getEthereumProvider(),
          account,
          operation,
        )
      if (!privy.authenticated)
        throw new Error('Sign in again to use your embedded wallet.')
      if (!config.gas_sponsorship) {
        const result = await sendTransaction(
          {
            to: operation.to,
            data: operation.data,
            value: 0,
            chainId: network().chainID,
          },
          {
            address: account,
            sponsor: false,
            uiOptions: { showWalletUIs: true },
          },
        )
        return result.hash
      }
      if (!(await confirm(account, operation))) throw cancelled()
      let saved = savedOperation(config.app_id, account)
      if (
        saved &&
        (saved.operation.action !== operation.action ||
          saved.operation.amount_units !== operation.amount_units ||
          saved.operation.to.toLowerCase() !== operation.to.toLowerCase() ||
          saved.operation.data !== operation.data)
      )
        throw new Error(
          'Resume your pending wallet operation before starting a different one.',
        )
      let prepared: Prepared
      if (saved) {
        operationID = saved.id
        prepared = await api(`/api/wallet/transactions/${saved.id}`)
      } else {
        prepared = await api('/api/wallet/transactions', {
          method: 'POST',
          body: JSON.stringify({
            action: operation.action,
            amount_units: operation.amount_units,
          }),
        })
        operationID = prepared.id
        saved = { id: prepared.id, account, operation }
        // Save the operation ID before any signing/broadcast, never the token or signature.
        journal(account, saved)
      }
      if (prepared.state === 'rejected') {
        journal(account, null)
        throw Object.assign(
          new Error(
            'Privy rejected this transaction. Check your wallet and gas sponsorship settings.',
          ),
          { code: 4001 },
        )
      }
      attemptedSubmit = prepared.state !== 'prepared'
      if (!prepared.hash && prepared.authorization) {
        checkAuthorization(
          prepared.authorization,
          config.app_id,
          prepared.id,
          operation,
        )
        const { signature } = await generateAuthorizationSignature(
          prepared.authorization,
        )
        attemptedSubmit = true
        prepared = await api(`/api/wallet/transactions/${prepared.id}/submit`, {
          method: 'POST',
          body: JSON.stringify({ signature }),
        })
      } else if (!prepared.hash && prepared.state === 'prepared') {
        throw new Error(
          'Authorization expired before submission. Start the operation again.',
        )
      }
      for (
        let attempt = 0;
        !prepared.hash && prepared.state !== 'rejected' && attempt < 15;
        attempt++
      ) {
        await new Promise((resolve) => setTimeout(resolve, 1500))
        prepared = await api(`/api/wallet/transactions/${operationID}`)
      }
      if (prepared.state === 'rejected') {
        journal(account, null)
        throw Object.assign(
          new Error(
            'The transaction was rejected. Check wallet and gas sponsorship configuration before trying again.',
          ),
          { code: 4001 },
        )
      }
      if (!prepared.hash || !/^0x[0-9a-fA-F]{64}$/.test(prepared.hash))
        throw new Error(
          'Transaction status is pending. Resume the same operation to check it; do not submit another payment.',
        )
      journal(account, null)
      window.dispatchEvent(
        new CustomEvent('tokenswoop:wallet-transaction', {
          detail: { id: operationID, hash: prepared.hash },
        }),
      )
      return prepared.hash
    } catch (e) {
      if (operationID && !attemptedSubmit) {
        try {
          await api(`/api/wallet/transactions/${operationID}`, {
            method: 'DELETE',
          })
          journal(account, null)
          operationID = ''
          Object.assign(e as Error, { code: 4001 })
        } catch {
          /* Preserve the journal if another tab may have submitted it. */
        }
      }
      if (operationID) Object.assign(e as Error, { operationId: operationID })
      throw e
    } finally {
      sending.current = false
    }
  }
  return (
    <PrivyConnectionContext.Provider
      value={{
        enabled: true,
        ready: privy.ready && connected.ready,
        busy,
        error,
        sessionVersion,
        login: async () => {
          bootstrap.current = true
          setLoginIntent(true)
          setError('')
          if (privy.authenticated) await authenticate(privy.user)
          else {
            setBusy(true)
            openLogin()
          }
        },
        logout: async () => {
          loggingOut.current = true
          setLoginIntent(false)
          try {
            await api('/api/auth/logout', { method: 'POST' }).catch((e) => {
              if (!(e instanceof APIError) || e.status !== 401) throw e
            })
            await privy.logout()
            bootstrap.current = false
            setError('')
            setSessionVersion((v) => v + 1)
          } finally {
            loggingOut.current = false
          }
        },
        embedded,
        sponsored: (account) => config.gas_sponsorship && embedded(account),
        send,
        receipt: walletReceipt,
        pending: (account) => savedOperation(config.app_id, account),
        resume: async (account) => {
          const value = savedOperation(config.app_id, account)
          if (!value) return ''
          return send(account, value.operation)
        },
      }}
    >
      {children}
      {walletSelection && (
        <div
          className="console c-wallet-layer"
          data-theme={document.documentElement.dataset.workspaceTheme || 'dark'}
        >
          <Modal title="Choose your wallet" onClose={() => closeSelection('')}>
            <p>
              Choose the wallet for this workspace. Balances, keys and bills
              belong to that wallet.
            </p>
            <div className="c-login-methods">
              {walletSelection.choices.map((w) => (
                <button
                  key={w.address}
                  className="c-login-choice"
                  onClick={() => closeSelection(w.address)}
                >
                  <span>
                    <strong>
                      {w.embedded ? 'Embedded wallet' : 'Connected wallet'}
                    </strong>
                    <small className="c-wallet-address">{w.address}</small>
                  </span>
                </button>
              ))}
            </div>
          </Modal>
        </div>
      )}
      {confirmation && (
        <div
          className="console c-wallet-layer"
          data-theme={document.documentElement.dataset.workspaceTheme || 'dark'}
        >
          <Modal
            title={confirmation.operation.title}
            onClose={() => closeConfirmation(false)}
          >
            <p>{confirmation.operation.description}</p>
            <dl className="c-wallet-confirm-details">
              <div>
                <dt>Wallet</dt>
                <dd>{short(confirmation.account)}</dd>
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
                <dd>Sponsored by TokenSwoop</dd>
              </div>
            </dl>
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
                Confirm transaction
              </button>
            </div>
          </Modal>
        </div>
      )}
    </PrivyConnectionContext.Provider>
  )
}
