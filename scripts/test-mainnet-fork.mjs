import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readEnvironment, networkValue } from './lib/network-env.mjs'

const root = fileURLToPath(new URL('../', import.meta.url)), contracts = path.join(root, 'contracts')
const env = readEnvironment(root)
const rpc = networkValue(env, 'mainnet', 'PAYMENT_RPC_URL')
if (!rpc) throw new Error('Configure MAINNET_PAYMENT_RPC_URL first')
const readiness = JSON.parse(fs.readFileSync(path.join(contracts, 'reports/mainnet-readiness.json'), 'utf8'))
const block = process.env.MAINNET_FORK_BLOCK || String(readiness.block)
if (!/^[1-9][0-9]*$/.test(block) || readiness.chainId !== 143) throw new Error('Run make mainnet-check first')
// forge test runs a local EVM. No signing key or broadcast flag is used.
const result = spawnSync(path.join(contracts, '.toolchain/bin/forge'), ['test', '--match-contract', 'Monad(USDC|ReserveVault)ForkTest', '--json', '--threads', '1', '--compute-units-per-second', '200'], {
  cwd: contracts, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024, timeout: 1200000,
  env: { ...env, FOUNDRY_PROFILE: 'default', RUN_MAINNET_FORK: 'true', MONAD_MAINNET_RPC_URL: rpc, MAINNET_FORK_BLOCK: block },
})
const redact = value => String(value || '').split(rpc).join('[private RPC]').replace(/https?:\/\/[^\s"'<>]*quiknode\.pro[^\s"'<>]*/g, '[private RPC]')
const output = redact(result.stdout)
fs.writeFileSync(path.join(contracts, 'reports/mainnet-fork-tests.json'), output)
fs.writeFileSync(path.join(contracts, 'reports/mainnet-fork-tests.stderr'), redact(result.stderr))
const summary = { testedAt: new Date().toISOString(), readOnlyRPC: true, chainId: 143, block: Number(block), passed: 0, failed: 0, skipped: 0, suites: [], sourceSHA256: {} }
for (const file of ['TokenSwoopPayG.sol', 'TokenSwoopPayGBatch.sol', 'TokenSwoopReserveVault.sol'])
  summary.sourceSHA256[file] = createHash('sha256').update(fs.readFileSync(path.join(contracts, 'src', file))).digest('hex')
try {
  for (const [name, suite] of Object.entries(JSON.parse(output))) {
    const tests = Object.entries(suite.test_results || {}).map(([name, result]) => ({ name, status: result.status, ...(result.reason ? { reason: result.reason } : {}) }))
    summary.suites.push({ name, tests })
    for (const { status } of tests) {
      if (status === 'Success') summary.passed++
      else if (status === 'Skipped') summary.skipped++
      else summary.failed++
    }
  }
} catch { console.error('Could not parse fork result. See redacted report files.') }
summary.success = result.status === 0 && summary.passed >= 16 && !summary.failed && !summary.skipped
fs.writeFileSync(path.join(contracts, 'reports/mainnet-fork-summary.json'), JSON.stringify(summary, null, 2) + '\n')
console.log(JSON.stringify(summary, null, 2))
process.exitCode = summary.success ? 0 : 1
