import test from 'node:test'
import assert from 'node:assert/strict'
import { siteRoute, siteURLs, homeHref } from '../src/siteRouting.ts'

const route = (url) => siteRoute(new URL(url))
test('public domains open the correct page at their root', () => {
  assert.deepEqual(route(siteURLs.home), { workspace: false })
  assert.deepEqual(route(siteURLs.mainnet), {
    workspace: true,
    expectedChainID: 143,
  })
  assert.deepEqual(route(siteURLs.testnet), {
    workspace: true,
    expectedChainID: 10143,
  })
  assert.equal(homeHref('app.tokenswoop.xyz'), siteURLs.home)
  assert.equal(homeHref('testnet.tokenswoop.xyz'), siteURLs.home)
})
test('old workspace links lose /app and retain the selected page and query', () => {
  assert.deepEqual(route(siteURLs.testnet + '/app?ref=demo#billing'), {
    workspace: true,
    expectedChainID: 10143,
    canonicalPath: '/?ref=demo#billing',
  })
  assert.deepEqual(route(siteURLs.mainnet + '/app/#docs'), {
    workspace: true,
    expectedChainID: 143,
    canonicalPath: '/#docs',
  })
  assert.deepEqual(route(siteURLs.home + '/app#models'), {
    workspace: false,
    redirect: siteURLs.mainnet + '/#models',
  })
})
test('local development retains both pages and exact path boundaries', () => {
  assert.deepEqual(route('http://localhost:5173/'), { workspace: false })
  assert.deepEqual(route('http://localhost:5173/app#docs'), { workspace: true })
  assert.deepEqual(route('http://127.0.0.1:5173/app/'), { workspace: true })
  assert.deepEqual(route('http://localhost:5173/apple'), { workspace: false })
  assert.equal(homeHref('localhost'), '/')
})
test('host matching is exact and cannot confuse public network domains', () => {
  assert.equal(route('https://APP.TOKENSWOOP.XYZ./').expectedChainID, 143)
  assert.deepEqual(route('https://app.tokenswoop.xyz.example.com/'), {
    workspace: false,
  })
  assert.deepEqual(route('https://testnet.tokenswoop.xyz.example.com/'), {
    workspace: false,
  })
})
