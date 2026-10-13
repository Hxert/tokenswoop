// After deployment: verify finalized chain state before enabling the mainnet
// profile. Does not change CHAIN_NETWORK, sign, broadcast, or copy test wallets.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { readEnvironment, networkValue, writeNetworkValues } from './lib/network-env.mjs'
import { mainnet, mainnetReader, keccak256 } from './lib/mainnet.mjs'

const root = fileURLToPath(new URL('../', import.meta.url)), contracts = path.join(root, 'contracts')
try {
  const env = readEnvironment(root)
  const options = {}
  for (let i = 2; i < process.argv.length; i += 2) {
    const key = process.argv[i]
    assert(['--payment', '--reserve'].includes(key) && process.argv[i + 1] && !Object.hasOwn(options, key), 'Usage: node scripts/configure-mainnet.mjs --payment ADDRESS --reserve ADDRESS')
    options[key] = process.argv[i + 1]
  }
  const value = key => networkValue(env, 'mainnet', key).toLowerCase()
  const deployment = {
    ...mainnet, batch: (options['--payment'] || value('PAYMENT_CONTRACT')).toLowerCase(),
    reserve: (options['--reserve'] || value('RESERVE_CONTRACT')).toLowerCase(),
    provider: value('PAYMENT_PROVIDER'), recipient: value('PAYMENT_RECIPIENT'), stopAuthority: value('PAYMENT_STOP_AUTHORITY'),
    operator: value('RESERVE_OPERATOR'), manager: value('RESERVE_MANAGER'),
    mode: 'debt', custody: 'vault', abiVersion: 4, testnetMock: false,
  }
  for (const key of ['batch', 'reserve', 'provider', 'stopAuthority', 'operator', 'manager', 'recipient'])
    assert(/^0x[0-9a-f]{40}$/.test(deployment[key]) && BigInt(deployment[key]) !== 0n, `Configure mainnet ${key}`)
  assert(deployment.provider !== deployment.stopAuthority && deployment.operator !== deployment.manager, 'Separate management and settlement roles required')
  assert(deployment.recipient !== deployment.provider && deployment.recipient !== deployment.batch, 'Separate revenue recipient required')
  assert.equal(value('PAYMENT_TOKEN'), mainnet.token.toLowerCase(), 'Mainnet currency must be native USDC')
  const file = path.join(contracts, 'deployments/monad-mainnet-reserve-debt.json')
  if (fs.existsSync(file)) {
    const previous = JSON.parse(fs.readFileSync(file, 'utf8'))
    assert.equal(previous.batch.toLowerCase(), deployment.batch, 'Refusing to replace an existing mainnet payment deployment')
    assert.equal(previous.reserve.toLowerCase(), deployment.reserve, 'Refusing to replace an existing mainnet reserve deployment')
  }
  const { rpc, call } = mainnetReader(env)
  assert.equal(BigInt(await rpc('eth_chainId', [])), 143n)
  const block = await rpc('eth_getBlockByNumber', ['finalized', false])
  assert(block?.number && block?.hash, 'Finalized block unavailable')
  const codeHashes = {}, sourceSHA256 = {}
  for (const [key, name] of [['batch', 'TokenSwoopPayGBatch'], ['reserve', 'TokenSwoopReserveVault']]) {
    const artifact = JSON.parse(fs.readFileSync(path.join(contracts, `out/${name}.sol/${name}.json`), 'utf8'))
    const code = await rpc('eth_getCode', [deployment[key], block.number])
    const normalize = hex => {
      const b = Buffer.from(hex.replace(/^0x/, ''), 'hex')
      for (const refs of Object.values(artifact.deployedBytecode.immutableReferences || {}))
        for (const { start, length } of refs) b.fill(0, start, start + length)
      return b.toString('hex')
    }
    assert.equal(normalize(code), normalize(artifact.deployedBytecode.object), `${key} runtime differs from compiled source`)
    codeHashes[key] = keccak256(code)
    sourceSHA256[name] = createHash('sha256').update(fs.readFileSync(path.join(contracts, `src/${name}.sol`))).digest('hex')
  }
  const tokenCode = await rpc('eth_getCode', [mainnet.token, block.number])
  assert(tokenCode !== '0x', 'USDC code missing')
  codeHashes.token = keccak256(tokenCode)
  for (const [to, name, expected] of [
    [deployment.batch, 'recipient', deployment.recipient], [deployment.batch, 'paymentToken', mainnet.token], [deployment.batch, 'provider', deployment.provider], [deployment.batch, 'owner', deployment.stopAuthority],
    [deployment.reserve, 'reserveToken', mainnet.token],
    [deployment.reserve, 'settlementOperator', deployment.operator], [deployment.reserve, 'manager', deployment.manager],
  ]) assert.equal((await call(to, `function ${name}() view returns (address)`, [], block.number)).toLowerCase(), expected.toLowerCase(), `${name} mismatch`)
  assert.equal(await call(mainnet.token, 'function decimals() view returns (uint8)', [], block.number), 6)
  assert.equal(await call(deployment.reserve, 'function custodyMode() pure returns (bytes32)', [], block.number), keccak256(new TextEncoder().encode('vault')))
  const assets = await call(deployment.reserve, 'function totalAssets() view returns (uint256)', [], block.number)
  const principal = await call(deployment.reserve, 'function totalPrincipal() view returns (uint256)', [], block.number)
  assert(assets >= principal, 'Reserve principal is not fully backed by idle USDC')
  assert.equal(assets, await call(mainnet.token, 'function balanceOf(address) view returns (uint256)', [deployment.reserve], block.number))
  const manifest = { ...deployment, verifiedAt: new Date().toISOString(), verifiedBlock: Number(BigInt(block.number)), verifiedBlockHash: block.hash, paymentCodeHash: codeHashes.batch, reserveCodeHash: codeHashes.reserve, tokenCodeHash: codeHashes.token, sourceSHA256 }
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file + '.tmp', JSON.stringify(manifest, null, 2) + '\n')
  fs.renameSync(file + '.tmp', file)
  writeNetworkValues(root, 'mainnet', {
    SETTLEMENT_ENABLED: 'true', PAYMENT_CONTRACT: deployment.batch, PAYMENT_CONTRACT_CODE_HASH: codeHashes.batch, PAYMENT_TOKEN_CODE_HASH: codeHashes.token,
    RESERVE_ENABLED: 'true', RESERVE_MODE: 'debt', RESERVE_CONTRACT: deployment.reserve, RESERVE_CODE_HASH: codeHashes.reserve,
    PAYMENT_RECIPIENT: deployment.recipient, RESERVE_CUSTODY: 'vault', RESERVE_POOL: '', RESERVE_ATOKEN: '', RESERVE_TREASURY: '',
  })
  console.log('Mainnet contracts and fixed roles verified at a finalized block. Mainnet profile enabled; CHAIN_NETWORK unchanged. No transaction broadcast.')
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}
