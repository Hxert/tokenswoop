import { monad, monadTestnet } from 'viem/chains'
import type { Chain } from 'viem'

let selected: 143 | 10143 = 10143

// The server is the sole network selector. App waits for /api/config before
// rendering wallet controls; no RPC credential is exposed to the browser.
export function configureNetwork(chainID: number) {
  if (chainID !== 143 && chainID !== 10143)
    throw new Error('Unsupported server network.')
  selected = chainID
}

// Preserve existing testnet journals; mainnet must never resume their operations.
export function networkStorageKey(prefix: string, ...parts: string[]) {
  return `${prefix}:${selected === 10143 ? '' : `${selected}:`}${parts.join(':')}`
}

export function network() {
  const testnet = selected === 10143
  const chain: Chain = testnet ? monadTestnet : monad
  return {
    chainID: selected,
    hexID: `0x${selected.toString(16)}`,
    caip2: `eip155:${selected}`,
    testnet,
    chain,
    name: testnet ? 'Monad Testnet' : 'Monad',
    symbol: testnet ? 'kUSDC' : 'USDC',
    explorer: testnet
      ? 'https://testnet.monadexplorer.com'
      : 'https://monadvision.com',
    publicRPC: testnet
      ? 'https://testnet-rpc.monad.xyz'
      : 'https://rpc.monad.xyz',
  }
}
