import { keccak256, toHex } from 'viem'
import type { Hex, LocalAccount } from 'viem'
import { api, APIError, loginWithWalletSignature } from './api.ts'
import type { Config } from './api.ts'
import { network, networkStorageKey } from './network.ts'
import { withMeraWallet, pendingMeraTransaction } from './meraWallet.ts'
import type { WalletOperation } from './walletTransport.ts'

export type MeraSponsorship = NonNullable<Config['mera']>
export type SponsoredMeraOperation = {
  id: string
  wallet: string
  chain_id: number
  implementation: string
  action: WalletOperation['action']
  amount_units: string
  operation_id: Hex
  deadline: number
  authorization_nonce: number | null
  state: string
  hash: string
  error_code: string
}
type Journal = { id: string; account: string; operation: WalletOperation }
const key = (account: string) =>
  networkStorageKey('tokenswoop:mera:sponsored:v1', account.toLowerCase())
const actions = {
  payment_approve: 0,
  reserve_approve: 1,
  reserve_deposit: 2,
  reserve_withdraw: 3,
} as const
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function sponsoredMeraPending(account: string): Journal | null {
  const text = localStorage.getItem(key(account))
  if (!text) return null
  const value = JSON.parse(text) as Journal
  if (
    !uuid.test(value.id) ||
    value.account.toLowerCase() !== account.toLowerCase() ||
    !(value.operation.action in actions)
  )
    throw new Error(
      'Could not read the pending Mera operation. Refresh to recover its server record.',
    )
  return value
}
export function clearSponsoredMera(account: string) {
  localStorage.removeItem(key(account))
}
export async function restoreSponsoredMera(
  account: string,
  config: MeraSponsorship,
) {
  return lock(account, () => restore(account, config))
}
async function restore(account: string, config: MeraSponsorship) {
  const chainID = network().chainID
  // Read our saved ID first: /pending no longer includes a finished operation.
  // Its result still needs to reach the user after a reload.
  const journal = sponsoredMeraPending(account)
  let o: SponsoredMeraOperation | null = null
  if (journal) {
    try {
      o = await api<SponsoredMeraOperation>(
        `/api/wallet/mera/transactions/${journal.id}`,
      )
    } catch (error) {
      if (!(error instanceof APIError) || error.status !== 404) throw error
    }
  }
  o ??= await api<SponsoredMeraOperation | null>('/api/wallet/mera/pending')
  if (chainID !== network().chainID)
    throw new Error('The network changed while recovering this operation.')
  if (!o) {
    clearSponsoredMera(account)
    return null
  }
  if (
    o.wallet.toLowerCase() !== account.toLowerCase() ||
    o.chain_id !== network().chainID ||
    o.implementation.toLowerCase() !== config.implementation.toLowerCase() ||
    !(o.action in actions)
  )
    throw new Error(
      'The pending wallet operation belongs to a different wallet or network.',
    )
  const spender =
    o.action === 'payment_approve' ? config.payment : config.reserve
  const approval = o.action.endsWith('approve')
  const data =
    (approval
      ? '0x095ea7b3' + spender.slice(2).toLowerCase().padStart(64, '0')
      : o.action === 'reserve_deposit'
        ? '0xb6b55f25'
        : '0x2e1a7d4d') + BigInt(o.amount_units).toString(16).padStart(64, '0')
  const operation: WalletOperation = {
    action: o.action,
    amount_units: o.amount_units,
    to: approval ? config.token : config.reserve,
    data,
    title: {
      payment_approve:
        o.amount_units === '0'
          ? 'Revoke payment allowance'
          : 'Approve payment allowance',
      reserve_approve: 'Reserve allowance',
      reserve_deposit: 'Deposit',
      reserve_withdraw: 'Withdrawal',
    }[o.action],
    description: `${o.action.replaceAll('_', ' ')}: ${BigInt(o.amount_units) / 1000000n}.${(BigInt(o.amount_units) % 1000000n).toString().padStart(6, '0')} ${network().symbol}.`,
  }
  localStorage.setItem(
    key(account),
    JSON.stringify({ id: o.id, account, operation }),
  )
  return o
}

export function meraTypedData(o: SponsoredMeraOperation) {
  return {
    domain: {
      name: 'TokenSwoop Mera',
      version: '1',
      chainId: o.chain_id,
      verifyingContract: o.wallet as Hex,
    },
    types: {
      WalletOperation: [
        { name: 'action', type: 'uint8' },
        { name: 'amount', type: 'uint256' },
        { name: 'id', type: 'bytes32' },
        { name: 'deadline', type: 'uint256' },
        { name: 'implementation', type: 'address' },
      ],
    },
    primaryType: 'WalletOperation',
    message: {
      action: actions[o.action],
      amount: BigInt(o.amount_units),
      id: o.operation_id,
      deadline: BigInt(o.deadline),
      implementation: o.implementation as Hex,
    },
  } as const
}
export function validateMeraOperation(
  o: SponsoredMeraOperation,
  journal: Journal,
  config: MeraSponsorship,
) {
  const expectedID = keccak256(
    toHex(
      `mera:${network().chainID}:${journal.account.toLowerCase()}:${journal.id}`,
    ),
  )
  const expectedTarget =
    o.action.startsWith('reserve_') && o.action !== 'reserve_approve'
      ? config.reserve
      : config.token
  if (
    !config.gas_sponsorship ||
    o.id !== journal.id ||
    o.wallet.toLowerCase() !== journal.account.toLowerCase() ||
    o.chain_id !== network().chainID ||
    o.implementation.toLowerCase() !== config.implementation.toLowerCase() ||
    o.action !== journal.operation.action ||
    o.amount_units !== journal.operation.amount_units ||
    o.operation_id !== expectedID ||
    !expectedTarget ||
    journal.operation.to.toLowerCase() !== expectedTarget.toLowerCase() ||
    !Number.isSafeInteger(o.deadline) ||
    o.deadline <= Date.now() / 1000 ||
    o.deadline > Date.now() / 1000 + 900 ||
    (o.authorization_nonce !== null &&
      (!Number.isSafeInteger(o.authorization_nonce) ||
        o.authorization_nonce < 0))
  )
    throw new Error(
      'The sponsored operation differs from the wallet action you confirmed.',
    )
  const spender =
    o.action === 'payment_approve' ? config.payment : config.reserve
  const selector = o.action.endsWith('approve')
    ? '0x095ea7b3' + spender.slice(2).toLowerCase().padStart(64, '0')
    : o.action === 'reserve_deposit'
      ? '0xb6b55f25'
      : '0x2e1a7d4d'
  if (
    (
      selector + BigInt(o.amount_units).toString(16).padStart(64, '0')
    ).toLowerCase() !== journal.operation.data.toLowerCase()
  )
    throw new Error('The wallet call does not match the sponsored operation.')
}
function terminal(o: SponsoredMeraOperation) {
  return ['reverted', 'expired', 'cancelled', 'rejected'].includes(o.state)
}
function retryable(error: unknown) {
  return (
    !(error instanceof APIError) || error.status >= 500 || error.status === 429
  )
}
function pause(signal?: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    signal?.throwIfAborted()
    const done = () => {
      signal?.removeEventListener('abort', aborted)
      resolve()
    }
    const timer = setTimeout(done, 1500)
    const aborted = () => {
      clearTimeout(timer)
      reject(signal!.reason)
    }
    signal?.addEventListener('abort', aborted, { once: true })
  })
}
async function waitForResult(
  journal: Journal,
  initial?: SponsoredMeraOperation,
  signal?: AbortSignal,
) {
  const chainID = network().chainID
  let o = initial
  for (;;) {
    signal?.throwIfAborted()
    if (network().chainID !== chainID)
      throw new Error('The network changed while waiting for this transaction.')
    if (o) {
      if (
        o.id !== journal.id ||
        o.wallet.toLowerCase() !== journal.account.toLowerCase() ||
        o.chain_id !== chainID
      )
        throw new Error(
          'The saved operation belongs to a different wallet or network.',
        )
      if (o.state === 'confirmed' && /^0x[0-9a-fA-F]{64}$/.test(o.hash)) {
        clearSponsoredMera(journal.account)
        return o.hash
      }
      if (terminal(o)) {
        clearSponsoredMera(journal.account)
        throw Object.assign(
          new Error(
            o.error_code === 'insufficient_wallet_balance'
              ? 'Insufficient wallet balance. The approval was not sent. Reduce the amount or add funds before trying again.'
              : o.state === 'reverted'
                ? 'The transaction reverted. No wallet operation was completed.'
                : 'This wallet operation expired or was rejected. Review the amount and try again.',
          ),
          {
            terminal: true,
            code: o.error_code,
            operationId: o.id,
            hash: o.hash,
          },
        )
      }
      if (o.state === 'review')
        throw Object.assign(
          new Error(
            'This transaction needs a platform check before its result can be confirmed. Its saved record has been retained.',
          ),
          { review: true, operationId: o.id, hash: o.hash },
        )
      // Only an explicit user action may unlock and sign a prepared draft.
      if (o.state === 'prepared') return null
      await pause(signal)
    }
    try {
      const timeout = AbortSignal.timeout(15000)
      o = await api<SponsoredMeraOperation>(
        `/api/wallet/mera/transactions/${journal.id}`,
        {
          signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
        },
      )
    } catch (error) {
      signal?.throwIfAborted()
      if (!retryable(error)) throw error
      // Reading status is safe to repeat; never create or sign another operation.
      o = undefined
      await pause(signal)
    }
  }
}
export async function watchSponsoredMera(
  account: string,
  signal?: AbortSignal,
) {
  return lock(account, async () => {
    const journal = sponsoredMeraPending(account)
    if (!journal) return null
    return waitForResult(journal, undefined, signal)
  })
}
async function lock<T>(account: string, run: () => Promise<T>): Promise<T> {
  if (!navigator.locks)
    throw new Error(
      'This browser cannot safely coordinate wallet transactions.',
    )
  return navigator.locks.request(
    networkStorageKey('tokenswoop:mera:transaction', account.toLowerCase()),
    { ifAvailable: true },
    async (held) => {
      if (!held)
        throw new Error('Finish the wallet operation in your other tab first.')
      return run()
    },
  )
}
async function submit(
  journal: Journal,
  config: MeraSponsorship,
  guard: () => Promise<void>,
  recovering = false,
) {
  const chainID = network().chainID
  const checkSession = async () => {
    await guard()
    if (chainID !== network().chainID)
      throw new Error(
        'The network changed. Review the operation and try again.',
      )
  }
  const needsMeraLogin = (error: unknown) =>
    error instanceof APIError &&
    error.status === 409 &&
    error.code === 'mera_wallet_required'
  let refreshedSession = false
  const refreshSession = async (account: LocalAccount) => {
    // Repair only a stale login for this wallet. Do not switch accounts or
    // silently relax the server's Mera session requirement.
    await checkSession()
    if (refreshedSession)
      throw new Error(
        'Your wallet session changed. Review the operation and try again.',
      )
    refreshedSession = true
    const user = await loginWithWalletSignature(
      account.address,
      (message) => account.signMessage({ message }),
      'mera',
    )
    if (user.wallet.toLowerCase() !== journal.account.toLowerCase())
      throw new Error(
        'The signed-in wallet changed. Review the current account.',
      )
    await checkSession()
  }
  const prepare = () =>
    api<SponsoredMeraOperation>('/api/wallet/mera/transactions', {
      method: 'POST',
      body: JSON.stringify({
        id: journal.id,
        action: journal.operation.action,
        amount_units: journal.operation.amount_units,
      }),
    })
  const authorize = async (
    o: SponsoredMeraOperation,
    account: LocalAccount,
  ) => {
    validateMeraOperation(o, journal, config)
    await checkSession()
    const authorization =
      o.authorization_nonce === null
        ? null
        : await account.signAuthorization!({
            address: config.implementation as Hex,
            chainId: o.chain_id,
            nonce: o.authorization_nonce,
          })
    const signature = await account.signTypedData(meraTypedData(o))
    // Exclude the viem legacy `v` bigint from the JSON authorization tuple.
    const body = JSON.stringify({
      signature,
      authorization: authorization && {
        address: authorization.address,
        chainId: authorization.chainId,
        nonce: authorization.nonce,
        r: authorization.r,
        s: authorization.s,
        yParity: authorization.yParity,
      },
    })
    const send = () =>
      api<SponsoredMeraOperation>(
        `/api/wallet/mera/transactions/${journal.id}/submit`,
        { method: 'POST', body },
      )
    const dispatch = async () => {
      try {
        return await send()
      } catch (error) {
        if (!retryable(error)) throw error
        // Leave the signing callback before polling so Mera can dispose of the
        // unlocked key. The server may already have saved this exact signature.
        return { ...o, state: 'submission_unknown' }
      }
    }
    await checkSession()
    try {
      return await dispatch()
    } catch (error) {
      if (!needsMeraLogin(error)) throw error
      // A restored draft may have been prepared in an earlier session. Reuse
      // this passkey unlock and the exact operation ID/signature on retry.
      await refreshSession(account)
      return dispatch()
    }
  }
  let o: SponsoredMeraOperation
  try {
    if (recovering) {
      try {
        // A signed operation can be recovered without another passkey prompt.
        o = await api<SponsoredMeraOperation>(
          `/api/wallet/mera/transactions/${journal.id}`,
        )
      } catch (error) {
        if (!(error instanceof APIError) || error.status !== 404) throw error
        o = await prepare()
      }
    } else o = await prepare()
  } catch (error) {
    if (!needsMeraLogin(error)) throw error
    await checkSession()
    o = await withMeraWallet(
      { expectedAddress: journal.account },
      async (account) => {
        // Old sessions predate the Mera source flag. One device confirmation
        // both renews that session and authorizes the action the user selected.
        await refreshSession(account)
        const prepared = await prepare()
        return prepared.state === 'prepared'
          ? authorize(prepared, account)
          : prepared
      },
    )
  }
  if (o.state === 'prepared') {
    validateMeraOperation(o, journal, config)
    await checkSession()
    o = await withMeraWallet({ expectedAddress: journal.account }, (account) =>
      authorize(o, account),
    )
  }
  const hash = await waitForResult(journal, o)
  if (!hash)
    throw new Error(
      'Confirm this wallet operation with your passkey to continue.',
    )
  return hash
}
export async function sendSponsoredMera(
  account: string,
  operation: WalletOperation,
  config: MeraSponsorship,
  guard: () => Promise<void>,
) {
  return lock(account, async () => {
    if (sponsoredMeraPending(account) || pendingMeraTransaction(account))
      throw new Error('Resume the pending wallet operation first.')
    const journal: Journal = { id: crypto.randomUUID(), account, operation }
    localStorage.setItem(key(account), JSON.stringify(journal))
    try {
      return await submit(journal, config, guard)
    } catch (error) {
      if (
        error instanceof APIError &&
        [400, 403, 404, 409, 422].includes(error.status)
      ) {
        try {
          await restore(account, config)
        } catch {
          /* Keep ambiguous state. */
        }
      }
      // Cancelling before submission is safe. An ambiguous submit always keeps its ID.
      if (
        (error as { code?: number }).code === 4001 ||
        /cancelled|canceled/i.test((error as Error).message)
      ) {
        try {
          await api(`/api/wallet/mera/transactions/${journal.id}`, {
            method: 'DELETE',
          })
          clearSponsoredMera(account)
        } catch {
          // Cancelling a passkey before session renewal may leave no server
          // draft at all. Recover before deciding whether to retain the ID.
          try {
            await restore(account, config)
          } catch {
            /* Keep ambiguous state. */
          }
        }
      }
      const pending = sponsoredMeraPending(account)
      if (pending) Object.assign(error as Error, { operationId: pending.id })
      throw error
    }
  })
}
export async function resumeSponsoredMera(
  account: string,
  config: MeraSponsorship,
  guard: () => Promise<void>,
) {
  return lock(account, async () => {
    const journal = sponsoredMeraPending(account)
    if (!journal) throw new Error('No sponsored operation to resume.')
    try {
      return await submit(journal, config, guard, true)
    } catch (error) {
      if (sponsoredMeraPending(account))
        Object.assign(error as Error, { operationId: journal.id })
      throw error
    }
  })
}
export async function reconcileSponsoredMera(account: string) {
  return lock(account, async () => {
    const pending = sponsoredMeraPending(account)
    if (!pending) return
    const o = await api<SponsoredMeraOperation>(
      `/api/wallet/mera/transactions/${pending.id}`,
    )
    if (terminal(o) || o.state === 'confirmed') clearSponsoredMera(account)
  })
}
