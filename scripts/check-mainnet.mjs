// Read-only verification of the actual endpoint, native USDC for direct custody.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { fileURLToPath } from 'node:url'
import { readEnvironment } from './lib/network-env.mjs'
import { mainnet, mainnetReader, keccak256 } from './lib/mainnet.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
try {
  const { rpc, call } = mainnetReader(readEnvironment(root))
  assert.equal(BigInt(await rpc('eth_chainId', [])), 143n, 'RPC is not Monad mainnet')
  const block = await rpc('eth_getBlockByNumber', ['finalized', false])
  assert(block?.hash && block?.number, 'Finalized block is unavailable')
  assert(Math.abs(Date.now() / 1000 - Number(BigInt(block.timestamp))) < 120, 'Finalized block is stale')
  const codeHashes = {}
  for (const [name, address] of Object.entries(mainnet).filter(([name]) => name !== 'chainId')) {
    const code = await rpc('eth_getCode', [address, block.number])
    assert(code && code !== '0x', `${name} has no runtime code`)
    codeHashes[name] = keccak256(code)
  }
  assert.equal(await call(mainnet.token, 'function decimals() view returns (uint8)', [], block.number), 6)
  assert.equal(await call(mainnet.token, 'function symbol() view returns (string)', [], block.number), 'USDC')
  // Verify indexed receipt scans supported at the same finalized block.
  await rpc('eth_getLogs', [{ address: mainnet.token, fromBlock: block.number, toBlock: block.number, topics: [keccak256(new TextEncoder().encode('Transfer(address,address,uint256)'))] }])
  const report = { checkedAt: new Date().toISOString(), readOnly: true, ...mainnet, symbol: 'USDC', decimals: 6, block: Number(BigInt(block.number)), blockHash: block.hash, codeHashes, reserveCustody: 'vault', requiredUSDCBuffer: '0', sources: ['https://developers.circle.com/stablecoins/usdc-contract-addresses'] }
  fs.mkdirSync(new URL('../contracts/reports/', import.meta.url), { recursive: true })
  fs.writeFileSync(new URL('../contracts/reports/mainnet-readiness.json', import.meta.url), JSON.stringify(report, null, 2) + '\n')
  console.log(JSON.stringify(report, null, 2))
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}
