import { network } from './network.ts'
export const reserveExplorer = () => network().explorer
export type ReserveAction = 'approve' | 'deposit' | 'withdraw'
export function reserveUnits(value: string): bigint {
  if (!/^(0|[1-9]\d{0,17})(\.\d{1,6})?$/.test(value))
    throw new Error('Enter a positive amount with up to 6 decimal places.')
  const [whole, fraction = ''] = value.split('.')
  const n = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0'))
  if (n <= 0n) throw new Error('Amount must be greater than zero.')
  return n
}
export function formatReserveUnits(value: string) {
  const n = BigInt(value),
    whole = n / 1_000_000n
  const fraction = (n % 1_000_000n)
    .toString()
    .padStart(6, '0')
    .replace(/0+$/, '')
  return `${whole.toLocaleString('en-US')}${fraction ? '.' + fraction : ''}`
}
export function reserveCall(
  action: ReserveAction,
  contract: string,
  units: bigint,
): string {
  const word = (n: bigint) => {
    if (n < 0n || n >= 2n ** 256n)
      throw new Error('Invalid transaction amount.')
    return n.toString(16).padStart(64, '0')
  }
  if (!/^0x[0-9a-fA-F]{40}$/.test(contract))
    throw new Error('Invalid reserve contract.')
  if (units <= 0n) throw new Error('Invalid transaction amount.')
  if (action === 'approve')
    return (
      '0x095ea7b3' +
      contract.slice(2).toLowerCase().padStart(64, '0') +
      word(units)
    )
  const selector = { deposit: '0xb6b55f25', withdraw: '0x2e1a7d4d' }[action]
  if (!selector) throw new Error('Unsupported reserve operation.')
  return selector + word(units)
}
export async function reserveWallet(account: string) {
  const wallet = window.ethereum
  if (!wallet)
    throw new Error('Open this page with your wallet extension to continue.')
  const accounts = (await wallet.request({
    method: 'eth_requestAccounts',
  })) as string[]
  if (accounts[0]?.toLowerCase() !== account.toLowerCase())
    throw new Error('Select the wallet used to sign in.')
  const chain = (await wallet.request({ method: 'eth_chainId' })) as string
  if (BigInt(chain) !== BigInt(network().chainID))
    throw new Error(`Switch your wallet to ${network().name}.`)
  // Repeat the account read after the chain check to catch wallet switching.
  const current = (await wallet.request({ method: 'eth_accounts' })) as string[]
  if (current[0]?.toLowerCase() !== account.toLowerCase())
    throw new Error('Wallet account changed. Select the signed-in account.')
  return wallet
}
