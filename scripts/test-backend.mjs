import path from 'node:path'
import fs from 'node:fs'
import { randomBytes } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { readEnvironment, networkValue } from './lib/network-env.mjs'

const root = fileURLToPath(new URL('../', import.meta.url)), backend = path.join(root, 'backend')
const env = readEnvironment(root)
// A separate database also isolates PostgreSQL advisory locks from the running
// worker. Random schemas alone do not isolate those locks. CI can provide its
// own dedicated TEST_DATABASE_URL instead of allowing CREATE DATABASE.
env.TEST_DATABASE_URL = process.env.TEST_DATABASE_URL || networkValue(env, 'testnet', 'DATABASE_URL')
env.TEST_REDIS_URL = process.env.TEST_REDIS_URL || networkValue(env, 'testnet', 'REDIS_URL')
if (!env.TEST_DATABASE_URL || !env.TEST_REDIS_URL) throw new Error('Configure testnet database and Redis for integration tests')
let temporaryDatabase
const base = new URL(env.TEST_DATABASE_URL)
const pgEnv = { ...process.env, PGHOST: base.hostname, PGPORT: base.port || '5432', PGDATABASE: 'postgres', PGPASSWORD: decodeURIComponent(base.password), PGSSLMODE: base.searchParams.get('sslmode') || 'prefer' }
if (base.username) pgEnv.PGUSER = decodeURIComponent(base.username)
const pgBin = process.env.PG_BIN || (fs.existsSync('/opt/homebrew/opt/postgresql@18/bin/createdb') ? '/opt/homebrew/opt/postgresql@18/bin' : '')
function databaseCommand(command, name) {
  const result = spawnSync(pgBin ? path.join(pgBin, command) : command, ['--', name], { env: pgEnv, encoding: 'utf8' })
  if (result.status !== 0) throw new Error(`${command} failed for isolated test database (connection details withheld)`)
}
if (!process.env.TEST_DATABASE_URL) {
  temporaryDatabase = 'tokenswoop_test_' + process.pid + '_' + randomBytes(4).toString('hex')
  databaseCommand('createdb', temporaryDatabase)
  base.pathname = '/' + temporaryDatabase
  env.TEST_DATABASE_URL = base.toString()
}
for (const key of Object.keys(env)) if (/^(MAINNET_|TESTNET_|CHAIN_|PAYMENT_|RESERVE_|PRIVY_|MERA_)/.test(key)) delete env[key]
env.SETTLEMENT_ENABLED = 'false'
env.DATABASE_URL = env.TEST_DATABASE_URL
env.REDIS_URL = env.TEST_REDIS_URL
env.GOCACHE = path.join(backend, '.cache/go-build')
env.GOMODCACHE = path.join(backend, '.cache/gomod')
env.GOPATH = path.join(backend, '.cache/gopath')
try {
  const result = spawnSync('go', ['test', '-race', '-p', '1', '-count=1', ...(process.argv.length > 2 ? process.argv.slice(2) : ['./...'])], { cwd: backend, env, stdio: 'inherit' })
  process.exitCode = result.status ?? 1
} finally {
  if (temporaryDatabase) databaseCommand('dropdb', temporaryDatabase)
}
