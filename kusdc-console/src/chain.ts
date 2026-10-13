import {
  BaseError,
  createPublicClient,
  createWalletClient,
  custom,
  getAddress,
  http,
  isAddress,
  parseAbi,
  parseEventLogs,
  zeroAddress,
} from 'viem'
import type { Address, EIP1193Provider, Hash, TransactionReceipt } from 'viem'
import { monadTestnet } from 'viem/chains'
import deployment from './deployment.json'

export type BrowserWallet = EIP1193Provider & {
  on?: (event: string, listener: (...args: unknown[]) => void) => void
  removeListener?: (
    event: string,
    listener: (...args: unknown[]) => void,
  ) => void
}
declare global {
  interface Window {
    ethereum?: BrowserWallet
  }
}

export const CHAIN_ID = 10143
export const RPC_URL = import.meta.env.VITE_KUSDC_RPC_URL || deployment.rpc
export const DEFAULT_TOKEN =
  import.meta.env.VITE_KUSDC_ADDRESS || deployment.token
export const explorer = 'https://testnet.monadscan.com'
export const tokenAbi = parseAbi([
  'function name() view returns (string)',
  'function symbol() view returns (string)',
  'function decimals() view returns (uint8)',
  'function owner() view returns (address)',
  'function paused() view returns (bool)',
  'function managementVersion() view returns (uint256)',
  'function totalSupply() view returns (uint256)',
  'function balanceOf(address) view returns (uint256)',
  'function isBlacklisted(address) view returns (bool)',
  'function mint(address to, uint256 amount)',
  'function confiscate(address account, uint256 amount)',
  'function clearBalance(address account, uint256 expectedBalance)',
  'event Transfer(address indexed from, address indexed to, uint256 value)',
  'error OwnableUnauthorizedAccount(address account)',
  'error ERC20InsufficientBalance(address sender, uint256 balance, uint256 needed)',
  'error ERC20InvalidReceiver(address receiver)',
  'error ERC20InvalidSender(address sender)',
  'error BlacklistedAccount(address account)',
  'error BalanceChanged(uint256 expected, uint256 actual)',
  'error EnforcedPause()',
  'error ZeroAmount()',
  'error BalanceLimitExceeded()',
])
export const publicClient = createPublicClient({
  chain: monadTestnet,
  transport: http(RPC_URL, { timeout: 12000, retryCount: 1 }),
  pollingInterval: 2000,
})
export type Mode = 'mint' | 'confiscate' | 'clearBalance'
export type TokenState = {
  token: Address
  owner: Address
  paused: boolean
  supply: bigint
  block: bigint
  target?: Address
  balance?: bigint
  targetBlocked: boolean
  ownerBlocked: boolean
}
export type Review = {
  mode: Mode
  token: Address
  target: Address
  account: Address
  amount: bigint
  balance: bigint
  block: bigint
}
export type Transaction = {
  id: string
  hash?: Hash
  mode: Mode
  token: Address
  target: Address
  account: Address
  amount: string
  at: string
  status: 'pending' | 'success' | 'failed' | 'unknown'
  message?: string
  block?: string
}

export function address(value: string): Address {
  if (!isAddress(value) || value.toLowerCase() === zeroAddress) {
    throw new Error('请输入有效的非零 EVM 地址。')
  }
  return getAddress(value)
}
export function sameAddress(a?: string, b?: string): boolean {
  return Boolean(a && b && a.toLowerCase() === b.toLowerCase())
}
export async function readState(
  token: Address,
  target?: Address,
): Promise<TokenState> {
  if ((await publicClient.getChainId()) !== CHAIN_ID)
    throw new Error('RPC 不是 Monad Testnet（10143），已阻止操作。')
  const block = await publicClient.getBlockNumber({ cacheTime: 0 })
  const read = <
    N extends
      | 'name'
      | 'symbol'
      | 'decimals'
      | 'owner'
      | 'paused'
      | 'managementVersion'
      | 'totalSupply',
  >(
    functionName: N,
  ) =>
    publicClient.readContract({
      address: token,
      abi: tokenAbi,
      functionName,
      blockNumber: block,
    })
  const [
    code,
    name,
    symbol,
    decimals,
    owner,
    paused,
    version,
    supply,
    balance,
    targetBlocked,
  ] = await Promise.all([
    publicClient.getCode({ address: token, blockNumber: block }),
    read('name'),
    read('symbol'),
    read('decimals'),
    read('owner'),
    read('paused'),
    read('managementVersion'),
    read('totalSupply'),
    target
      ? publicClient.readContract({
          address: token,
          abi: tokenAbi,
          functionName: 'balanceOf',
          args: [target],
          blockNumber: block,
        })
      : undefined,
    target
      ? publicClient.readContract({
          address: token,
          abi: tokenAbi,
          functionName: 'isBlacklisted',
          args: [target],
          blockNumber: block,
        })
      : false,
  ])
  if (
    !code ||
    code === '0x' ||
    name !== 'TokenSwoop Test USDC' ||
    symbol !== 'kUSDC' ||
    decimals !== 6 ||
    version !== 1n
  ) {
    throw new Error('该地址不是新版、6 位精度的管理版 kUSDC。')
  }
  const ownerBlocked = await publicClient.readContract({
    address: token,
    abi: tokenAbi,
    functionName: 'isBlacklisted',
    args: [owner],
    blockNumber: block,
  })
  return {
    token,
    owner,
    paused,
    supply,
    block,
    target,
    balance,
    targetBlocked,
    ownerBlocked,
  }
}
export async function walletState(
  wallet: BrowserWallet,
): Promise<{ account?: Address; chain?: number }> {
  const [accounts, chain] = await Promise.all([
    wallet.request({ method: 'eth_accounts' }),
    wallet.request({ method: 'eth_chainId' }),
  ])
  return {
    account: accounts[0] ? getAddress(accounts[0]) : undefined,
    chain: Number(BigInt(chain)),
  }
}
export async function switchNetwork(wallet: BrowserWallet): Promise<void> {
  const client = createWalletClient({
    chain: monadTestnet,
    transport: custom(wallet, { retryCount: 0 }),
  })
  try {
    await client.switchChain({ id: CHAIN_ID })
  } catch (error) {
    const code =
      error instanceof BaseError
        ? error.walk(
            (e) =>
              typeof e === 'object' &&
              e !== null &&
              'code' in e &&
              e.code === 4902,
          )
        : error
    if (
      code &&
      typeof code === 'object' &&
      'code' in code &&
      code.code === 4902
    ) {
      await client.addChain({ chain: monadTestnet })
      await client.switchChain({ id: CHAIN_ID })
    } else throw error
  }
}
export async function validateReview(
  review: Review,
  wallet: BrowserWallet,
): Promise<TokenState> {
  const connected = await walletState(wallet)
  if (connected.chain !== CHAIN_ID)
    throw new Error('请先将钱包切换到 Monad Testnet。')
  if (!sameAddress(connected.account, review.account))
    throw new Error('钱包账户已变化，请重新预览。')
  const state = await readState(review.token, review.target)
  if (!sameAddress(state.owner, review.account))
    throw new Error('只有合约 owner 可以管理 kUSDC。')
  if (state.paused) throw new Error('代币已暂停，请先恢复合约再操作。')
  if (state.ownerBlocked || state.targetBlocked)
    throw new Error('管理员或目标地址处于代币黑名单中。')
  if (review.mode !== 'mint' && review.amount > state.balance!)
    throw new Error('余额不足，请刷新目标余额。')
  if (review.mode === 'clearBalance' && review.amount !== state.balance)
    throw new Error('余额已变化，请重新预览清空操作。')
  await publicClient.simulateContract({
    address: review.token,
    abi: tokenAbi,
    functionName: review.mode,
    args: [review.target, review.amount],
    account: review.account,
  })
  return state
}
export async function submit(
  review: Review,
  wallet: BrowserWallet,
): Promise<Hash> {
  await validateReview(review, wallet)
  const client = createWalletClient({
    chain: monadTestnet,
    transport: custom(wallet, { retryCount: 0 }),
  })
  try {
    return await client.writeContract({
      address: review.token,
      abi: tokenAbi,
      functionName: review.mode,
      args: [review.target, review.amount],
      account: review.account,
      chain: monadTestnet,
    })
  } catch (error) {
    throw new SubmissionError(error)
  }
}
export class SubmissionError extends Error {
  constructor(cause: unknown) {
    super(message(cause))
    this.cause = cause
  }
}
export function receiptMatches(
  transaction: Transaction,
  receipt: TransactionReceipt,
): boolean {
  if (
    receipt.status !== 'success' ||
    !sameAddress(receipt.to ?? undefined, transaction.token) ||
    !sameAddress(receipt.from, transaction.account)
  )
    return false
  return parseEventLogs({
    abi: tokenAbi,
    eventName: 'Transfer',
    logs: receipt.logs.filter((log) =>
      sameAddress(log.address, transaction.token),
    ),
  }).some(
    (log) =>
      log.args.value === BigInt(transaction.amount) &&
      (transaction.mode === 'mint'
        ? sameAddress(log.args.from, zeroAddress) &&
          sameAddress(log.args.to, transaction.target)
        : sameAddress(log.args.from, transaction.target) &&
          sameAddress(log.args.to, zeroAddress)),
  )
}
export function message(error: unknown): string {
  if (error instanceof BaseError) {
    const text = error.shortMessage
    if (/user rejected|denied/i.test(text)) return '已取消钱包确认。'
    if (/BalanceChanged/i.test(error.message))
      return '余额已变化，请刷新后重新预览。'
    if (/OwnableUnauthorizedAccount/i.test(error.message))
      return '该钱包不是合约管理员。'
    if (/EnforcedPause/i.test(error.message)) return '代币已暂停。'
    if (/BlacklistedAccount/i.test(error.message))
      return '目标地址或管理员处于黑名单中。'
    return text
  }
  return error instanceof Error ? error.message : '操作未完成，请重试。'
}
