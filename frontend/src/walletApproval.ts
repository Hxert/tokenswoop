import { createPublicClient, formatUnits } from 'viem'
import { chainRpcTransport } from './chainRpc.ts'
import { network } from './network.ts'
import type { WalletOperation } from './walletTransport.ts'

const chain = createPublicClient({ transport: chainRpcTransport() })
const balanceABI = [
  {
    type: 'function',
    name: 'balanceOf',
    stateMutability: 'view',
    inputs: [{ type: 'address', name: 'owner' }],
    outputs: [{ type: 'uint256' }],
  },
] as const

export async function checkApprovalBalance(
  account: string,
  operation: WalletOperation,
) {
  if (!['payment_approve', 'reserve_approve'].includes(operation.action)) return
  const amount = BigInt(operation.amount_units)
  if (amount === 0n) return // Revocation must work even with an empty wallet.
  const selected = network()
  try {
    const balance = await chain.readContract({
      address: operation.to as `0x${string}`,
      abi: balanceABI,
      functionName: 'balanceOf',
      args: [account as `0x${string}`],
      blockTag: 'latest',
    })
    if (network().chainID !== selected.chainID)
      throw new Error('The network changed. Review the approval and try again.')
    if (balance < amount)
      throw Object.assign(
        new Error(
          `Insufficient wallet balance. You have ${formatUnits(balance, 6)} ${selected.symbol}; the approval amount is ${formatUnits(amount, 6)} ${selected.symbol}.`,
        ),
        { code: 'insufficient_wallet_balance' },
      )
  } catch (error) {
    // This check runs before opening a wallet or creating any transaction.
    Object.assign(error as Error, { beforeSubmission: true })
    throw error
  }
}
