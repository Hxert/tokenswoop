// Explicit, resumable Testnet test of mint/confiscate/clear. No private keys are logged.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { randomBytes } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

process.chdir(path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..'))
const read = p => JSON.parse(fs.readFileSync(p, 'utf8'))
const d = read('deployments/monad-testnet.json'), wallets = read('deployments/testnet-wallets.json')
assert.equal(d.chainId, 10143)
assert.equal(d.tokenManagementVersion, 1)
const file = 'reports/kusdc-management-v1/admin-smoke.json'
const j = fs.existsSync(file) ? read(file) : { token: d.kUSDC, recipient: '0x' + randomBytes(20).toString('hex'), transactions: {} }
assert.equal(j.token, d.kUSDC)
const save = () => { fs.writeFileSync(file + '.tmp', JSON.stringify(j, null, 2) + '\n', { mode: 0o600 }); fs.renameSync(file + '.tmp', file) }
save()
function cast(...args) {
  try { return execFileSync('.toolchain/bin/cast', args.map(String), { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 45000 }).trim() }
  catch { throw new Error('Admin wallet command failed; sensitive output suppressed. Resume the same smoke test.') }
}
let last = 0
async function rpc(method, params) {
  await new Promise(resolve => setTimeout(resolve, Math.max(0, last + 180 - Date.now())))
  last = Date.now()
  const response = await fetch(d.rpc, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }), signal: AbortSignal.timeout(15000) })
  assert(response.ok)
  const body = await response.json()
  assert(!body.error, 'RPC error')
  return body.result
}
async function call(signature, ...args) { return rpc('eth_call', [{ to: d.kUSDC, data: cast('calldata', signature, ...args) }, 'latest']) }
async function confirmed(hash) {
  for (let i = 0; i < 60; i++) {
    const receipt = await rpc('eth_getTransactionReceipt', [hash])
    if (receipt) {
      assert.equal(BigInt(receipt.status), 1n)
      const block = await rpc('eth_getBlockByNumber', ['finalized', false])
      if (BigInt(block.number) >= BigInt(receipt.blockNumber)) return receipt
    }
    await new Promise(resolve => setTimeout(resolve, 1000))
  }
  throw new Error('Awaiting receipt; resume this SAME smoke test')
}
async function send(name, signature, quantity, expectedBalance) {
  let tx = j.transactions[name]
  if (!tx) {
    const owner = wallets.accounts.deployer
    const nonce = await rpc('eth_getTransactionCount', [owner.address, 'pending'])
    assert.equal(nonce, await rpc('eth_getTransactionCount', [owner.address, 'latest']))
    const data = cast('calldata', signature, j.recipient, quantity)
    const gas = BigInt(await rpc('eth_estimateGas', [{ from: owner.address, to: d.kUSDC, data }])) * 120n / 100n
    const price = BigInt(await rpc('eth_gasPrice', []))
    assert(gas * price < 50_000_000_000_000_000n)
    const raw = cast('mktx', d.kUSDC, signature, j.recipient, quantity, '--rpc-url', d.rpc, '--chain', '10143', '--legacy', '--nonce', BigInt(nonce), '--gas-limit', gas, '--gas-price', price, '--keystore', owner.keystore, '--password-file', wallets.passwordFile, '--from', owner.address)
    tx = { raw, hash: cast('keccak', raw), amount: String(quantity) }
    j.transactions[name] = tx
    save()
  }
  if (!await rpc('eth_getTransactionReceipt', [tx.hash])) {
    try { await rpc('eth_sendRawTransaction', [tx.raw]) } catch { /* Query only the previously journaled hash. */ }
  }
  const receipt = await confirmed(tx.hash)
  tx.block = receipt.blockNumber
  if (!tx.checked) {
    assert.equal(BigInt(await call('balanceOf(address)', j.recipient)), BigInt(expectedBalance))
    tx.checked = true
    save()
  }
  console.log(`${name}: finalized ${tx.hash}`)
}
assert.equal(BigInt(await rpc('eth_chainId', [])), 10143n)
if (!j.complete) {
  if (!j.before) { j.before = { supply: BigInt(await call('totalSupply()')).toString() }; save() }
  await send('mint', 'mint(address,uint256)', 13_456_789, 13_456_789)
  await send('confiscate', 'confiscate(address,uint256)', 3_456_789, 10_000_000)
  await send('clear', 'clearBalance(address,uint256)', 10_000_000, 0)
  assert.equal(BigInt(await call('totalSupply()')), BigInt(j.before.supply))
  j.complete = true
  j.completedAt = new Date().toISOString()
  save()
}
console.log('Testnet admin smoke passed: 13.456789 issued, 3.456789 confiscated, remaining 10 cleared. Recipient balance is zero; supply restored.')
