// Opt-in integration QA against an isolated Anvil chain 10143 and a dedicated browser.
// The browser wallet forwards exclusively to loopback Anvil; it never signs Testnet transactions.
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createPublicClient, http, parseAbi, encodeFunctionData } from 'viem'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const localRPC = 'http://127.0.0.1:8547'
const browserURL = 'http://127.0.0.1:5175/'
const local = JSON.parse(await fs.readFile('/private/tmp/kusdc-console-local-deployment.json', 'utf8'))
const client = createPublicClient({ transport: http(localRPC) })
assert.equal(await client.getChainId(), 10143)
const rpc = async (method, params = []) => {
  const response = await fetch(localRPC, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }) })
  const result = await response.json()
  if (result.error) throw new Error(result.error.message)
  return result.result
}
const accounts = await rpc('eth_accounts')
const owner = accounts[0], recipient = accounts[2]
const abi = parseAbi(['function mint(address,uint256)', 'function balanceOf(address) view returns (uint256)', 'function totalSupply() view returns (uint256)'])
const balance = () => client.readContract({ address: local.deployedTo, abi, functionName: 'balanceOf', args: [recipient] })
const targets = await (await fetch('http://127.0.0.1:9223/json/list')).json()
const socket = new WebSocket(targets.find(item => item.type === 'page').webSocketDebuggerUrl)
await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }) })
let id = 0
const requests = new Map(), runtimeErrors = [], checks = []
socket.addEventListener('message', ({ data }) => {
  const result = JSON.parse(data)
  if (result.method === 'Runtime.exceptionThrown') runtimeErrors.push(result.params.exceptionDetails.text)
  const pending = requests.get(result.id)
  if (!pending) return
  clearTimeout(pending.timeout); requests.delete(result.id)
  result.error ? pending.reject(new Error(result.error.message)) : pending.resolve(result.result)
})
function send(method, params = {}) {
  return new Promise((resolve, reject) => {
    const requestID = ++id
    const timeout = setTimeout(() => { requests.delete(requestID); reject(new Error('Timeout ' + method)) }, 15000)
    requests.set(requestID, { resolve, reject, timeout })
    socket.send(JSON.stringify({ id: requestID, method, params }))
  })
}
async function evaluate(expression) {
  const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text)
  return result.result.value
}
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
async function wait(expression) {
  const deadline = Date.now() + 12000
  do { if (await evaluate(expression)) return; await sleep(100) } while (Date.now() < deadline)
  throw new Error('Condition failed: ' + expression)
}
function check(name, passed, detail) {
  checks.push({ name, passed, detail }); console.log(`${passed ? 'PASS' : 'FAIL'} ${name}${detail ? ' ' + JSON.stringify(detail) : ''}`)
  assert(passed, name)
}
async function click(selector) {
  const point = await evaluate(`(() => { const el=document.querySelector(${JSON.stringify(selector)});el.scrollIntoView({block:'center',behavior:'instant'});const r=el.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`)
  await send('Input.dispatchMouseEvent', { type: 'mouseMoved', ...point })
  await send('Input.dispatchMouseEvent', { type: 'mousePressed', ...point, button: 'left', clickCount: 1 })
  await send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...point, button: 'left', clickCount: 1 })
}
async function fill(selector, value) {
  await evaluate(`(() => {const el=document.querySelector(${JSON.stringify(selector)});Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(el,${JSON.stringify(value)});el.dispatchEvent(new Event('input',{bubbles:true}));})()`)
}
async function screenshot(name) {
  const { data } = await send('Page.captureScreenshot', { format: 'png' })
  await fs.writeFile(path.join(root, 'artifacts', name + '.png'), Buffer.from(data, 'base64'))
}
async function confirm(successCount) {
  await click('form .primary')
  await wait('!!document.querySelector(".review .primary")')
  await click('.review .primary')
  await wait(`document.querySelectorAll('.status.success').length===${successCount} && !document.querySelector('.wallet-button').disabled`)
}
const bridge = `(() => {
  const listeners=new Map();let connected=false;let selected=${JSON.stringify(owner)};let chain='0x279f';let sends=0;
  window.__wallet={set(account,newChain){selected=account;chain=newChain;connected=true;for(const event of ['accountsChanged','chainChanged'])for(const handler of listeners.get(event)||[])handler(event==='accountsChanged'?[selected]:chain);},count(){return sends;}};
  window.ethereum={on(event,handler){const list=listeners.get(event)||[];list.push(handler);listeners.set(event,list);},removeListener(event,handler){listeners.set(event,(listeners.get(event)||[]).filter(h=>h!==handler));},async request({method,params=[]}){
    if(method==='eth_requestAccounts'){connected=true;return [selected];}
    if(method==='eth_accounts')return connected?[selected]:[];
    if(method==='eth_chainId')return chain;
    if(method==='wallet_switchEthereumChain'){window.__wallet.set(selected,params[0].chainId);return null;}
    if(method==='eth_sendTransaction')sends++;
    const response=await fetch(${JSON.stringify(localRPC)},{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',id:1,method,params})});const result=await response.json();if(result.error)throw result.error;return result.result;
  }};
})();`
try {
  await fs.mkdir(path.join(root, 'artifacts'), { recursive: true })
  await send('Page.enable'); await send('Runtime.enable')
  const script = await send('Page.addScriptToEvaluateOnNewDocument', { source: bridge })
  await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 1000, deviceScaleFactor: 1, mobile: false })
  await send('Page.navigate', { url: browserURL }); await send('Page.bringToFront')
  await evaluate('localStorage.removeItem("kusdc-console.transactions.v1")')
  await send('Page.reload')
  await wait('document.querySelector(".contract-status")?.textContent.includes("正常运行")')
  check('Disconnected wallet cannot manage funds', await evaluate('document.querySelector("form .primary").disabled'))
  await click('.wallet-button')
  await fill('#target', recipient)
  await wait('!document.querySelector("form .primary").disabled')
  await fill('#amount', '0.0000001'); await click('form .primary')
  await wait('document.querySelector(".feedback.error")?.textContent.includes("6 位小数")')
  check('Seven decimal places cannot submit a transaction', await evaluate('window.__wallet.count()') === 0)
  await fill('#amount', '100.000001'); await confirm(1)
  check('Mint changes the real chain balance by the exact amount', await balance() === 100000001n)
  await click('.tabs button:nth-child(2)')
  await fill('#amount', '12.345678'); await confirm(2)
  check('Confiscation destroys the exact amount', await balance() === 87654323n)
  await click('.tabs button:nth-child(3)')
  await click('form .primary'); await wait('!!document.querySelector(".review .primary")')
  await rpc('eth_sendTransaction', [{ from: owner, to: local.deployedTo, data: encodeFunctionData({ abi, functionName: 'mint', args: [recipient, 1000000n] }) }])
  const callsBefore = await evaluate('window.__wallet.count()')
  await click('.review .primary')
  await wait('document.querySelector(".feedback.error")?.textContent.includes("余额已变化")')
  check('Changed balance invalidates clear preview before wallet submission', await balance() === 88654323n && await evaluate('window.__wallet.count()') === callsBefore)
  await click('.balance-row button'); await wait('!document.querySelector("form .primary").disabled')
  await confirm(3)
  check('Clear destroys the entire balance and restores total supply to zero', await balance() === 0n && await client.readContract({ address: local.deployedTo, abi, functionName: 'totalSupply' }) === 0n)
  check('Each successful operation corresponds to one wallet submission', await evaluate('window.__wallet.count()') === 3)
  await evaluate(`window.__wallet.set(${JSON.stringify(accounts[1])},'0x279f')`)
  await wait('document.querySelector("form .primary").disabled')
  check('Non-owner wallet is blocked', await evaluate('document.querySelector(".overview").textContent.includes("不是合约 owner")'))
  await evaluate(`window.__wallet.set(${JSON.stringify(owner)},'0x1')`)
  await wait('!!document.querySelector(".network-alert")')
  check('Wrong wallet chain is blocked', await evaluate('document.querySelector("form .primary").disabled'))
  await click('.network-alert button'); await wait('!document.querySelector(".network-alert")')
  await send('Page.reload')
  await wait('document.querySelectorAll(".status.success").length===3')
  check('Confirmed transaction history survives a reload', true)
  await send('Page.removeScriptToEvaluateOnNewDocument', { identifier: script.identifier })
  await send('Page.navigate', { url: 'http://127.0.0.1:5174/' })
  await wait('document.querySelector(".contract-status")?.textContent.includes("正常运行")')
  check('Default console reads the real deployed Monad Testnet token', await evaluate('document.querySelector("#token").value.toLowerCase()') === '0x37693c6a557620c4196d91cca28c61af454f30de')
  for (const width of [1280, 768, 390, 320]) {
    await send('Emulation.setDeviceMetricsOverride', { width, height: 1000, deviceScaleFactor: 1, mobile: false }); await sleep(120)
    const layout = await evaluate('({overflow:document.documentElement.scrollWidth>innerWidth,forms:document.querySelector(".operation-panel").getBoundingClientRect().right<=innerWidth})')
    check('Layout fits at ' + width + 'px', !layout.overflow && layout.forms, layout)
    if (width === 1280) await screenshot('console-desktop')
    if (width === 390) await screenshot('console-mobile')
  }
  check('No application runtime errors', runtimeErrors.length === 0, runtimeErrors)
  await fs.writeFile(path.join(root, 'artifacts/browser-checks.json'), JSON.stringify({ passed: true, checks, localRPC, checkedAt: new Date().toISOString() }, null, 2) + '\n')
} catch (error) {
  console.log(await evaluate('({error:document.querySelector(".feedback.error")?.textContent,notice:document.querySelector(".feedback.notice")?.textContent,review:!!document.querySelector(".review"),statuses:Array.from(document.querySelectorAll(".status")).map(e=>e.textContent),sends:window.__wallet?.count()})').catch(() => ({})))
  await screenshot('console-check-failure').catch(() => {})
  throw error
} finally { socket.close() }
