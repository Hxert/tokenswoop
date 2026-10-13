// Monad Testnet migration. Sources stay local; no explorer verification is performed.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
process.chdir(root)
const run = 'kusdc-management-v1'
const directory = `reports/${run}`
const journalFile = `${directory}/migration.json`
fs.mkdirSync(directory, { recursive: true })
const read = p => JSON.parse(fs.readFileSync(p, 'utf8'))
const save = (p, value) => fs.writeFileSync(p, JSON.stringify(value, null, 2) + '\n')
const wallets = read('deployments/testnet-wallets.json')
const rpcURL = process.env.MONAD_TESTNET_RPC_URL || 'https://testnet-rpc.monad.xyz'
assert(new URL(rpcURL).protocol === 'https:')
const paymentStage = `deployments/${run}-pending.json`
const reserveStage = `deployments/${run}-reserve-pending.json`
const journal = fs.existsSync(journalFile) ? read(journalFile) : {
  chainId: 10143, createdAt: new Date().toISOString(), sourceSubmitted: false,
  oldPayment: read('deployments/monad-testnet.json'),
  oldReserve: read('deployments/monad-testnet-reserve.json'), transactions: {},
}
assert(journal.chainId === 10143 && wallets.chainId === 10143)
save(journalFile, journal)
let requestID = 0
async function rpc(method, params) {
  const response = await fetch(rpcURL, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: ++requestID, method, params }), signal: AbortSignal.timeout(15000) })
  assert(response.ok, 'RPC unavailable')
  const body = await response.json()
  assert(!body.error, body.error?.message || 'RPC error')
  return body.result
}
function cast(...args) {
  try { return execFileSync('.toolchain/bin/cast', args.map(String), { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 90000 }).trim() }
  catch { throw new Error('Wallet command failed. Inspect the migration journal; sensitive subprocess output suppressed.') }
}
async function call(to, signature) {
  return rpc('eth_call', [{ to, data: cast('sig', signature) }, 'latest'])
}
async function confirmed(hash) {
  for (let tries = 0; tries < 60; tries++) {
    const receipt = await rpc('eth_getTransactionReceipt', [hash])
    if (receipt) {
      assert.equal(BigInt(receipt.status), 1n, 'Migration transaction reverted')
      const block = await rpc('eth_getBlockByNumber', ['finalized', false])
      if (block && BigInt(block.number) >= BigInt(receipt.blockNumber)) return receipt
    }
    await new Promise(resolve => setTimeout(resolve, 1000))
  }
  throw new Error(`Receipt awaiting finality: ${hash}. Resume the SAME migration.`)
}
function cli(script, args) {
  try {
    const output = execFileSync(process.execPath, [`scripts/${script}.mjs`, ...args, '--rpc', rpcURL], { encoding: 'utf8', maxBuffer: 10_000_000, stdio: ['ignore', 'pipe', 'pipe'] })
    console.log(output.trim())
  } catch { throw new Error(`${script} deployment stopped. Resume this same migration after inspecting its private progress journal and broadcast receipts.`) }
}
async function step(name, operation) {
  if (journal[name]) return
  console.log(`Migration: ${name}`)
  await operation()
  journal[name] = true
  save(journalFile, journal)
}
await step('preflight', async () => {
  assert.equal(BigInt(await rpc('eth_chainId', [])), 10143n)
  assert.equal(BigInt(await call(journal.oldReserve.reserve, 'totalPrincipal()')), 0n, 'Existing user collateral must be withdrawn before retirement')
  for (const role of ['deployer', 'provider']) {
    const account = wallets.accounts[role].address
    assert.equal(await rpc('eth_getTransactionCount', [account, 'pending']), await rpc('eth_getTransactionCount', [account, 'latest']), `${role} has a pending transaction`)
    assert(BigInt(await rpc('eth_getBalance', [account, 'latest'])) > 100_000_000_000_000_000n, `${role} needs Test MON`)
  }
  const balances = execFileSync('/opt/homebrew/opt/postgresql@18/bin/psql', ['-h', '127.0.0.1', '-p', '55432', '-d', 'tokenswoop', '-X', '-A', '-t', '-c', "SELECT (SELECT count(*) FROM bills WHERE state <> 'paid') + (SELECT count(*) FROM payment_attempts WHERE state='pending') + (SELECT count(*) FROM requests WHERE state IN ('running','pending'));"], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  assert.equal(balances, '0', 'Drain existing requests and bills before migration')
})
await step('paymentsDeployed', async () => {
  if (!fs.existsSync(paymentStage)) {
    const progress = `${directory}/payment-deployment.json`
    cli('payg', ['deploy', '--manifest', paymentStage, '--deployment-journal', progress, ...(fs.existsSync(progress) ? ['--resume'] : [])])
  }
  const d = read(paymentStage)
  assert.equal(BigInt(await call(d.kUSDC, 'managementVersion()')), 1n)
})
await step('reserveDeployed', async () => {
  if (!fs.existsSync(reserveStage)) {
    const progress = `${directory}/reserve-deployment.json`
    cli('reserve', ['deploy', '--manifest', reserveStage, '--payment-manifest', paymentStage, '--deployment-journal', progress, ...(fs.existsSync(progress) ? ['--resume'] : [])])
  }
  cli('reserve', ['verify', '--manifest', reserveStage, '--payment-manifest', paymentStage])
})
await step('activated', async () => {
  const payment = read(paymentStage), reserve = read(reserveStage)
  assert.equal(payment.kUSDC.toLowerCase(), reserve.kUSDC.toLowerCase())
  assert.equal(payment.paygBatch.toLowerCase(), reserve.paygBatch.toLowerCase())
  fs.mkdirSync('deployments/history', { recursive: true })
  save(`deployments/history/${run}-previous.json`, journal.oldPayment)
  save(`deployments/history/${run}-reserve-previous.json`, journal.oldReserve)
  save('deployments/monad-testnet.json', { ...payment, supersedes: { kUSDC: journal.oldPayment.kUSDC, payg: journal.oldPayment.payg, paygBatch: journal.oldPayment.paygBatch }, migrationReport: journalFile })
  save('deployments/monad-testnet-reserve.json', { ...reserve, supersedesReserve: journal.oldReserve.reserve, migrationReport: journalFile })
  execFileSync(process.execPath, ['../scripts/configure-testnet-payments.mjs'], { stdio: ['ignore', 'pipe', 'pipe'] })
  execFileSync(process.execPath, ['../kusdc-console/scripts/sync-deployment.mjs'], { stdio: ['ignore', 'pipe', 'pipe'] })
})
await step('oldContractsRetired', async () => {
  const deployer = wallets.accounts.deployer
  for (const [name, contract] of [ ['oldPayG', journal.oldPayment.payg], ['oldBatch', journal.oldPayment.paygBatch], ['oldReserve', journal.oldReserve.reserve], ['oldToken', journal.oldPayment.kUSDC] ]) {
    if (journal.transactions[name]) { await confirmed(journal.transactions[name]); continue }
    if (BigInt(await call(contract, 'paused()')) === 1n) continue
    const receipt = JSON.parse(cast('send', contract, 'pause()', '--rpc-url', rpcURL, '--chain', '10143', '--from', deployer.address, '--keystore', deployer.keystore, '--password-file', wallets.passwordFile, '--json'))
    journal.transactions[name] = receipt.transactionHash
    save(journalFile, journal)
    await confirmed(receipt.transactionHash)
  }
})
journal.completedAt = new Date().toISOString()
save(journalFile, journal)
console.log('Management-enabled kUSDC migration complete. Sources remain private. Restart the API and worker to load the new payment configuration.')
