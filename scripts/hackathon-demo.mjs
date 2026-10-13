#!/usr/bin/env node
// A real API client for the recorded demo. No mock responses or automatic POST retries.
import fs from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { consumeChatStream } from '../frontend/src/chatStream.ts'

const root = fileURLToPath(new URL('../', import.meta.url))
const directory = path.join(root, '.local/hackathon-demo')
const keyFile = path.join(directory, 'api-key')
const origin = 'https://app.tokenswoop.xyz'
const prompt = 'Give me a three-step launch checklist for an AI customer-support assistant. Keep it under sixty words.'
const args = process.argv.slice(2)
const command = args.shift() || 'help'
const value = name => {
  const i = args.indexOf(name)
  return i >= 0 ? args[i + 1] : undefined
}
const modelID = value('--model') || 'gpt-6-sol'
const printable = text => String(text).replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, '')

async function json(url) {
  const response = await fetch(url, { redirect: 'error', signal: AbortSignal.timeout(15000) })
  if (!response.ok) throw new Error(`Read-only check failed: HTTP ${response.status}`)
  return response.json()
}
async function save(name, data) {
  await fs.mkdir(directory, { recursive: true, mode: 0o700 })
  await fs.writeFile(path.join(directory, name), JSON.stringify(data, null, 2) + '\n', { mode: 0o600 })
}
async function readiness() {
  const [health, config, models, tiers] = await Promise.all([
    json(origin + '/health/ready'), json(origin + '/api/config'),
    json(origin + '/api/models'), json(origin + '/api/tiers'),
  ])
  if (config.chain_id !== 143 || config.network?.testnet)
    throw new Error('The recording target is not Monad mainnet; no paid request sent.')
  if (health.status !== 'ready' || !config.settlement_enabled)
    throw new Error('The mainnet API or settlement is unavailable.')
  const model = models.data.find(m => m.id === modelID && m.enabled && m.pricing_configured)
  if (!model) throw new Error(`Model unavailable: ${modelID}`)
  return { checkedAt: new Date().toISOString(), origin, health, config, model, tiers: tiers.data }
}
async function check() {
  const result = await readiness()
  console.log('TokenSwoop recording readiness — read-only\n')
  console.log(`Workspace:    ${origin}`)
  console.log('Network:      Monad mainnet (143) · USDC')
  console.log(`API:          ${result.health.status}`)
  console.log(`Mera gas:     ${result.config.mera?.gas_sponsorship ? 'enabled' : 'unavailable'}`)
  console.log(`Reserve:      ${result.config.reserve_enabled ? 'enabled' : 'unavailable'}`)
  console.log(`Model:        ${result.model.id}`)
  console.log(`List price:   ${result.model.input_price} input / ${result.model.output_price} output USDC per million tokens`)
  console.log(`Live tiers:   ${result.tiers.map(t => `${t.id} (${t.discount_bps / 100}% discount, ${t.concurrency} concurrent)`).join(', ')}`)
  try { await fs.access(keyFile); console.log('Demo API key: saved locally (value hidden)') }
  catch { console.log('Demo API key: not configured; run the setup command before recording') }
  const wallet = value('--wallet')
  if (wallet) {
    if (!/^0x[0-9a-fA-F]{40}$/.test(wallet)) throw new Error('Invalid wallet address.')
    const { mainnetReader } = await import('./lib/mainnet.mjs')
    const { readEnvironment } = await import('./lib/network-env.mjs')
    const reader = mainnetReader(readEnvironment(root))
    if (Number(BigInt(await reader.rpc('eth_chainId', []))) !== 143) throw new Error('RPC chain mismatch.')
    const block = await reader.rpc('eth_getBlockByNumber', ['latest', false])
    const contracts = result.config.mera
    const units = (n, decimals) => {
      const digits = BigInt(n).toString().padStart(decimals + 1, '0')
      return digits.slice(0, -decimals) + '.' + digits.slice(-decimals)
    }
    const mon = units(await reader.rpc('eth_getBalance', [wallet, block.number]), 18)
    const balance = units(await reader.call(contracts.token, 'function balanceOf(address) view returns (uint256)', [wallet], block.number), 6)
    const allowance = units(await reader.call(contracts.token, 'function allowance(address,address) view returns (uint256)', [wallet, contracts.payment], block.number), 6)
    const available = units(await reader.call(contracts.reserve, 'function withdrawableOf(address) view returns (uint256)', [wallet], block.number), 6)
    console.log(`\nDemo wallet:  ${wallet}\nMON:          ${mon}\nUSDC:         ${balance}\nAPI allowance:${allowance} USDC\nWithdrawable: ${available} USDC`)
    result.wallet = { address: wallet, block: block.number, mon, usdc: balance, allowance, withdrawable: available }
  }
  await save('readiness.json', result)
  console.log('\nSaved .local/hackathon-demo/readiness.json. No paid requests or transactions were sent.')
}
function hiddenKey() {
  if (!process.stdin.isTTY) throw new Error('Run setup in an interactive terminal; the API key is hidden while typing.')
  return new Promise((resolve, reject) => {
    let key = ''
    const previousRaw = process.stdin.isRaw
    const finish = (error) => {
      process.stdin.off('data', data)
      process.stdin.setRawMode(previousRaw)
      process.stdin.pause()
      process.stdout.write('\n')
      error ? reject(error) : resolve(key.trim())
    }
    const data = chunk => {
      for (const character of chunk.toString()) {
        if (character === '\u0003') return finish(new Error('Setup cancelled.'))
        if (character === '\r' || character === '\n') return finish()
        if (character === '\u007f' || character === '\b') key = key.slice(0, -1)
        else if (/^[\x20-\x7e]$/.test(character)) key += character
      }
    }
    process.stdout.write('Paste the dedicated demo API key (hidden), then press Enter: ')
    process.stdin.setRawMode(true)
    process.stdin.resume()
    process.stdin.on('data', data)
  })
}
async function setup() {
  const key = await hiddenKey()
  if (!/^sk-[A-Za-z0-9_-]{16,}$/.test(key)) throw new Error('Expected a TokenSwoop sk- API key.')
  await fs.mkdir(directory, { recursive: true, mode: 0o700 })
  await fs.writeFile(keyFile, key + '\n', { mode: 0o600 })
  await fs.chmod(keyFile, 0o600)
  console.log('Dedicated key saved locally. It will not appear in demo output.')
}
async function run() {
  const ready = await readiness()
  let key
  try { key = (await fs.readFile(keyFile, 'utf8')).trim() }
  catch { throw new Error('Run node scripts/hackathon-demo.mjs setup before recording.') }
  if (!/^sk-[A-Za-z0-9_-]{16,}$/.test(key)) throw new Error('The saved demo key is invalid.')
  const record = { startedAt: new Date().toISOString(), origin, model: ready.model.id, prompt, status: 'sending', requestId: null, receipt: null, output: '' }
  const name = 'request-' + Date.now() + '.json'
  await save(name, record)
  const header = () => console.log(`TokenSwoop · Live API demo\n\nEndpoint: ${origin}/v1/chat/completions\nModel: ${record.model}\n\nYou: ${prompt}\n\nAssistant:`)
  header()
  try {
    // One POST only. On an uncertain result, inspect its receipt before a new run.
    const response = await fetch(origin + '/v1/chat/completions', {
      method: 'POST', redirect: 'error', signal: AbortSignal.timeout(120000),
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + key },
      body: JSON.stringify({ model: record.model, messages: [{ role: 'user', content: prompt }], stream: true, stream_options: { include_usage: true }, max_completion_tokens: 384 }),
    })
    const requestId = response.headers.get('X-Request-ID')
    if (/^[0-9a-f-]{36}$/i.test(requestId || '')) {
      record.requestId = requestId
      record.receipt = origin + '/#receipt?request=' + encodeURIComponent(requestId)
    }
    record.status = 'streaming'
    await save(name, record)
    if (!response.ok) {
      const body = await response.json().catch(() => ({}))
      throw new Error(`HTTP ${response.status}: ${body.error?.message || 'Request rejected'}`)
    }
    if (!response.body || !response.headers.get('content-type')?.includes('text/event-stream'))
      throw new Error('The server did not return an SSE stream.')
    await consumeChatStream(response.body, delta => {
      record.output += delta.content
      process.stdout.write(printable(delta.content))
    }, () => {
      record.output = ''
      if (process.stdout.isTTY) { process.stdout.write('\u001b[2J\u001b[H'); header() }
      else console.log('\n[The upstream attempt restarted; its partial answer was replaced.]')
    })
    record.status = 'stream_completed'
    console.log('\n\nResponse complete. Confirm the final charge and settlement in the workspace.')
  } catch (error) {
    record.status = 'needs_receipt_check'
    process.exitCode = 1
    console.error('\n' + printable(error.message))
    console.error('No automatic resubmission. Check Usage before sending another request.')
  } finally {
    record.finishedAt = new Date().toISOString()
    await save(name, record)
    if (record.requestId) console.log('\nRequest ID: ' + record.requestId)
    if (record.receipt) console.log('Receipt: ' + record.receipt)
    console.log('Recording receipt: .local/hackathon-demo/' + name)
  }
}
try {
  if (command === 'check') await check()
  else if (command === 'setup') await setup()
  else if (command === 'run') await run()
  else console.log(`TokenSwoop recording client (Node 24+)\n\n  node scripts/hackathon-demo.mjs check [--wallet 0x...]\n    Read-only production checks; wallet checks use the configured mainnet RPC.\n  node scripts/hackathon-demo.mjs setup\n    Privately save a dedicated project API key before recording.\n  node scripts/hackathon-demo.mjs run [--model gpt-6-sol]\n    Send ONE real, paid streaming request to Monad mainnet workspace.\n\nRun is not a simulation. Billing uses upstream usage after completion.\nThe client prints the matching receipt URL and does not retry the POST.\nDo not show the setup step or the API key file in your recording.`)
} catch (error) {
  console.error(printable(error.message))
  process.exitCode = 1
}
