import {
  createPasskeyWithPrfOutput,
  getPasskeyPrfOutput,
  createSecp256k1SigningSession,
} from '@category-labs/mera'
import type {
  PasskeyCredentialMetadata,
  WebAuthnClient,
} from '@category-labs/mera'
import { toViemAccount } from '@category-labs/mera/viem'
import { HDKey } from '@scure/bip32'
import { entropyToMnemonic, mnemonicToSeedSync } from '@scure/bip39'
import { wordlist } from '@scure/bip39/wordlists/english.js'
import {
  createPublicClient,
  createWalletClient,
  keccak256,
  parseTransaction,
  recoverTransactionAddress,
} from 'viem'
import type { Hex, LocalAccount, TransactionSerialized } from 'viem'
import { network, networkStorageKey } from './network.ts'
import { chainRpcTransport } from './chainRpc.ts'
import type { WalletOperation } from './walletTransport'
import { passkeyError } from './passkeyError.ts'

// Fixed derivation scheme: exported phrases recover this address in standard EVM wallets.
export const meraDerivationPath = "m/44'/60'/0'/0/0"
export const meraCredentialsKey = 'tokenswoop:mera:credentials:v1'
const journalKey = (wallet: string) =>
  networkStorageKey('tokenswoop:mera:transaction', wallet.toLowerCase())
type SavedCredential = PasskeyCredentialMetadata & {
  wallet: string
  rpId: string
}
export type MeraPending = {
  id: Hex
  account: string
  operation: WalletOperation
  raw: Hex
}
const transport = chainRpcTransport()
const rpc = createPublicClient({
  transport,
})

export async function withPrfAccount<T>(
  entropy: Uint8Array,
  use: (account: LocalAccount, phrase: string) => Promise<T>,
  expectedAddress?: string,
): Promise<T> {
  let root: HDKey | undefined, child: HDKey | undefined
  let seed: Uint8Array | undefined
  let session: ReturnType<typeof createSecp256k1SigningSession> | undefined
  try {
    if (entropy.length !== 32) throw new Error('Invalid passkey output.')
    const phrase = entropyToMnemonic(entropy, wordlist)
    seed = mnemonicToSeedSync(phrase)
    root = HDKey.fromMasterSeed(seed)
    child = root.derive(meraDerivationPath)
    const key = child.privateKey
    if (!key) throw new Error('Could not derive the passkey wallet.')
    try {
      session = createSecp256k1SigningSession({ privateKey: key })
    } finally {
      key.fill(0)
    }
    const account = toViemAccount(session)
    if (
      expectedAddress &&
      account.address.toLowerCase() !== expectedAddress.toLowerCase()
    )
      throw new Error(
        'This passkey belongs to a different wallet. Choose the passkey used to sign in.',
      )
    return await use(account, phrase)
  } finally {
    session?.end()
    child?.wipePrivateData()
    root?.wipePrivateData()
    seed?.fill(0)
    entropy.fill(0)
  }
}

function credentials(): SavedCredential[] {
  try {
    const values = JSON.parse(localStorage.getItem(meraCredentialsKey) || '[]')
    return Array.isArray(values)
      ? values.filter(
          (v) =>
            typeof v.credentialId === 'string' &&
            /^[A-Za-z0-9_-]+$/.test(v.credentialId) &&
            /^0x[0-9a-fA-F]{40}$/.test(v.wallet) &&
            typeof v.rpId === 'string',
        )
      : []
  } catch {
    return []
  }
}
export function passkeyAvailable() {
  return (
    window.isSecureContext &&
    typeof PublicKeyCredential !== 'undefined' &&
    !!navigator.credentials
  )
}
export async function withMeraWallet<T>(
  options: {
    create?: boolean
    expectedAddress?: string
    webAuthnClient?: WebAuthnClient
  },
  use: (account: LocalAccount, phrase: string) => Promise<T>,
) {
  if (!passkeyAvailable())
    throw new Error('Use a browser with passkey support on HTTPS or localhost.')
  const rpId = location.hostname
  const known = options.expectedAddress
    ? credentials().find(
        (v) =>
          v.rpId === rpId &&
          v.wallet.toLowerCase() === options.expectedAddress?.toLowerCase(),
      )
    : undefined
  try {
    const result = options.create
      ? await createPasskeyWithPrfOutput({
          rp: { id: rpId, name: 'TokenSwoop' },
          user: {
            name: 'TokenSwoop ' + new Date().toISOString().slice(0, 10),
            displayName: 'TokenSwoop wallet',
          },
          webAuthnClient: options.webAuthnClient,
        })
      : await getPasskeyPrfOutput({
          rpId,
          credential: known,
          webAuthnClient: options.webAuthnClient,
        })
    return await withPrfAccount(
      result.prfOutput,
      async (account, phrase) => {
        // Store only public credential metadata. A new device can discover the synced passkey.
        const list = credentials().filter(
          (v) => v.rpId !== rpId || v.credentialId !== result.credentialId,
        )
        list.push({
          credentialId: result.credentialId,
          rpId,
          wallet: account.address.toLowerCase(),
        })
        try {
          localStorage.setItem(meraCredentialsKey, JSON.stringify(list))
        } catch {
          /* Discovery still works. */
        }
        return use(account, phrase)
      },
      options.expectedAddress,
    )
  } catch (error) {
    throw passkeyError(error, !!options.create)
  }
}

export function pendingMeraTransaction(account: string): MeraPending | null {
  const stored = localStorage.getItem(journalKey(account))
  if (!stored) return null
  try {
    const value = JSON.parse(stored)
    if (
      value.account?.toLowerCase() === account.toLowerCase() &&
      /^0x[0-9a-fA-F]{64}$/.test(value.id) &&
      /^0x[0-9a-fA-F]+$/.test(value.raw) &&
      value.operation?.action
    )
      return value
  } catch {
    /* Never silently replace a damaged pending transaction. */
  }
  throw new Error(
    'The saved wallet transaction is unreadable. Check wallet activity before starting another transaction.',
  )
}
export function clearMeraTransaction(account: string, hash: string) {
  if (pendingMeraTransaction(account)?.id.toLowerCase() === hash.toLowerCase())
    localStorage.removeItem(journalKey(account))
}
export async function validateMeraTransaction(value: MeraPending) {
  const tx = parseTransaction(value.raw)
  if (
    keccak256(value.raw) !== value.id ||
    tx.chainId !== network().chainID ||
    tx.to?.toLowerCase() !== value.operation.to.toLowerCase() ||
    tx.data?.toLowerCase() !== value.operation.data.toLowerCase() ||
    (tx.value ?? 0n) !== 0n ||
    (
      await recoverTransactionAddress({
        serializedTransaction: value.raw as TransactionSerialized,
      })
    ).toLowerCase() !== value.account.toLowerCase()
  )
    throw new Error(
      'The saved transaction does not match the reviewed wallet operation.',
    )
}
async function resumeTransaction(account: string) {
  const value = pendingMeraTransaction(account)
  if (!value) throw new Error('No pending passkey wallet transaction.')
  await validateMeraTransaction(value)
  if ((await rpc.getChainId()) !== network().chainID)
    throw new Error(`The RPC is not on ${network().name}.`)
  try {
    const receipt = await rpc.getTransactionReceipt({ hash: value.id })
    clearMeraTransaction(account, value.id)
    if (receipt.status === 'reverted')
      throw Object.assign(new Error('The saved transaction reverted.'), {
        code: 4001,
      })
    return value.id
  } catch (error) {
    if ((error as Error).name !== 'TransactionReceiptNotFoundError') throw error
  }
  try {
    // Re-broadcast the exact signed bytes and nonce. Never build a second deposit on retry.
    await rpc.sendRawTransaction({ serializedTransaction: value.raw })
  } catch {
    try {
      await rpc.getTransaction({ hash: value.id })
    } catch {
      throw new Error(
        'Transaction status is uncertain. Resume this saved transaction to retry the same signed operation.',
      )
    }
  }
  return value.id
}
async function transactionLock<T>(account: string, action: () => Promise<T>) {
  if (!navigator.locks)
    throw new Error(
      'This browser cannot safely coordinate wallet transactions. Use a browser with Web Locks support.',
    )
  return navigator.locks.request(
    journalKey(account),
    { ifAvailable: true },
    (lock) => {
      if (!lock)
        throw new Error(
          'A wallet transaction is in progress in another tab. Finish it before continuing.',
        )
      return action()
    },
  )
}
export function resumeMeraTransaction(account: string) {
  return transactionLock(account, () => resumeTransaction(account))
}
export async function sendMeraTransaction(
  account: string,
  operation: WalletOperation,
  checkSession: () => Promise<void>,
) {
  return transactionLock(account, async () => {
    if (pendingMeraTransaction(account))
      throw new Error(
        'Resume the pending wallet transaction before starting another one.',
      )
    await checkSession()
    await withMeraWallet({ expectedAddress: account }, async (signer) => {
      if ((await rpc.getChainId()) !== network().chainID)
        throw new Error(`The RPC is not on ${network().name}.`)
      const client = createWalletClient({
        chain: network().chain,
        account: signer,
        transport,
      })
      const request = await client.prepareTransactionRequest({
        to: operation.to as Hex,
        data: operation.data as Hex,
        value: 0n,
      })
      await checkSession()
      const raw = await client.signTransaction(request)
      const value: MeraPending = { id: keccak256(raw), account, operation, raw }
      await validateMeraTransaction(value)
      // Only the already-authorized transaction is journaled, never a PRF output or private key.
      // Persist before broadcast; storage failure prevents sending.
      localStorage.setItem(journalKey(account), JSON.stringify(value))
    })
    await checkSession()
    return resumeTransaction(account)
  })
}
