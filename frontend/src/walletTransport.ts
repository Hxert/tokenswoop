import { network } from './network.ts'

export type EthereumProvider = {
  request: (args: { method: string; params?: unknown[] }) => Promise<unknown>
}
export type WalletOperation = {
  action:
    | 'payment_approve'
    | 'reserve_approve'
    | 'reserve_deposit'
    | 'reserve_withdraw'
  amount_units: string
  to: string
  data: string
  title: string
  description: string
}
export type WalletReceipt = { status: string } | null

export async function prepareProvider(
  provider: EthereumProvider,
  account: string,
) {
  const accounts = (await provider.request({
    method: 'eth_requestAccounts',
  })) as string[]
  if (accounts[0]?.toLowerCase() !== account.toLowerCase())
    throw new Error('Select the wallet used to sign in.')
  if (
    BigInt((await provider.request({ method: 'eth_chainId' })) as string) !==
    BigInt(network().chainID)
  ) {
    try {
      await provider.request({
        method: 'wallet_switchEthereumChain',
        params: [{ chainId: network().hexID }],
      })
    } catch (error) {
      if ((error as { code?: number }).code !== 4902) throw error
      await provider.request({
        method: 'wallet_addEthereumChain',
        params: [
          {
            chainId: network().hexID,
            chainName: network().name,
            nativeCurrency: { name: 'Monad', symbol: 'MON', decimals: 18 },
            rpcUrls: [network().publicRPC],
            blockExplorerUrls: [network().explorer],
          },
        ],
      })
    }
  }
  if (
    BigInt((await provider.request({ method: 'eth_chainId' })) as string) !==
    BigInt(network().chainID)
  )
    throw new Error(`Switch your wallet to ${network().name}.`)
  const current = (await provider.request({
    method: 'eth_accounts',
  })) as string[]
  if (current[0]?.toLowerCase() !== account.toLowerCase())
    throw new Error('Wallet account changed. Select the signed-in account.')
  return provider
}
export async function sendExternal(
  provider: EthereumProvider,
  account: string,
  op: WalletOperation,
) {
  await prepareProvider(provider, account)
  const hash = await provider.request({
    method: 'eth_sendTransaction',
    params: [
      {
        from: account,
        to: op.to,
        data: op.data,
        value: '0x0',
        chainId: network().hexID,
      },
    ],
  })
  if (typeof hash !== 'string' || !/^0x[0-9a-fA-F]{64}$/.test(hash))
    throw new Error(
      'No transaction hash returned. Check wallet activity before retrying.',
    )
  return hash
}

export type SponsoredAuthorization = {
  version: 1
  method: 'POST'
  url: string
  body: {
    method: string
    chain_type: string
    caip2: string
    sponsor: boolean
    reference_id: string
    params: { transaction: { to: string; data: string; value: string } }
  }
  headers: {
    'privy-app-id': string
    'privy-idempotency-key': string
    'privy-request-expiry': string
  }
}
// Validate what the user is about to authorize, even though the backend constructs it.
export function checkAuthorization(
  auth: SponsoredAuthorization,
  appID: string,
  id: string,
  op: WalletOperation,
) {
  const tx = auth.body?.params?.transaction
  if (
    auth.version !== 1 ||
    auth.method !== 'POST' ||
    !/^https:\/\/api\.privy\.io\/v1\/wallets\/[a-zA-Z0-9_-]+\/rpc$/.test(
      auth.url,
    ) ||
    auth.headers['privy-app-id'] !== appID ||
    auth.headers['privy-idempotency-key'] !== id ||
    auth.body.method !== 'eth_sendTransaction' ||
    auth.body.chain_type !== 'ethereum' ||
    auth.body.caip2 !== network().caip2 ||
    auth.body.sponsor !== true ||
    tx?.to.toLowerCase() !== op.to.toLowerCase() ||
    tx.data.toLowerCase() !== op.data.toLowerCase() ||
    tx.value !== '0x0' ||
    Object.keys(tx).sort().join(',') !== 'data,to,value'
  )
    throw new Error(
      'Wallet transaction details changed. Refresh and review the operation.',
    )
  if (
    auth.body.reference_id !== id ||
    Object.keys(auth.body).sort().join(',') !==
      'caip2,chain_type,method,params,reference_id,sponsor' ||
    Object.keys(auth.body.params).join(',') !== 'transaction'
  )
    throw new Error('Unexpected wallet authorization fields.')
  if (
    !Number.isFinite(Number(auth.headers['privy-request-expiry'])) ||
    Number(auth.headers['privy-request-expiry']) <= Date.now()
  )
    throw new Error(
      'This wallet authorization expired. Check wallet activity before starting another operation.',
    )
}
