import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import type { Address } from 'viem'
import {
  CHAIN_ID,
  DEFAULT_TOKEN,
  SubmissionError,
  address,
  explorer,
  message,
  publicClient,
  readState,
  receiptMatches,
  sameAddress,
  submit,
  switchNetwork,
  validateReview,
  walletState,
} from './chain'
import type { Mode, Review, TokenState, Transaction } from './chain'
import { formatAmount, parseAmount } from './amounts'

const STORAGE = 'kusdc-console.transactions.v1'
const modes: { id: Mode; name: string; mark: string; description: string }[] = [
  {
    id: 'mint',
    name: '发放',
    mark: '+',
    description: '向指定地址铸造 kUSDC，总供应量同步增加。',
  },
  {
    id: 'confiscate',
    name: '没收',
    mark: '−',
    description: '销毁指定数量的 kUSDC，无需持有人授权。',
  },
  {
    id: 'clearBalance',
    name: '清空',
    mark: '↘',
    description: '销毁该地址的全部 kUSDC，余额归零。',
  },
]
const modeName = (mode: Mode) => modes.find((item) => item.id === mode)!.name
const short = (value: string) => `${value.slice(0, 6)}…${value.slice(-4)}`
function loadHistory(): Transaction[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(STORAGE) || '[]')
    if (!Array.isArray(parsed)) return []
    return parsed
      .filter((item): item is Transaction => {
        if (!item || typeof item !== 'object') return false
        const validAddress = (value: unknown) =>
          typeof value === 'string' && /^0x[\da-fA-F]{40}$/.test(value)
        return (
          typeof item.id === 'string' &&
          typeof item.at === 'string' &&
          /^\d+$/.test(item.amount) &&
          validAddress(item.token) &&
          validAddress(item.target) &&
          validAddress(item.account) &&
          (!item.hash || /^0x[\da-fA-F]{64}$/.test(item.hash)) &&
          modes.some((mode) => mode.id === item.mode) &&
          ['pending', 'success', 'failed', 'unknown'].includes(item.status)
        )
      })
      .slice(0, 30)
  } catch {
    return []
  }
}

export default function App() {
  const [token, setToken] = useState(DEFAULT_TOKEN)
  const [target, setTarget] = useState('')
  const [amount, setAmount] = useState('100')
  const [mode, setMode] = useState<Mode>('mint')
  const [state, setState] = useState<TokenState>()
  const [account, setAccount] = useState<Address>()
  const [chain, setChain] = useState<number>()
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [review, setReview] = useState<Review>()
  const [history, setHistory] = useState<Transaction[]>(loadHistory)
  const journal = useRef(history)
  const locked = useRef(false)
  const readGeneration = useRef(0)
  const current = modes.find((item) => item.id === mode)!
  const owner = sameAddress(account, state?.owner)
  const networkOK = chain === CHAIN_ID
  const unsettled = history.some(
    (item) =>
      sameAddress(item.account, account) &&
      sameAddress(item.token, token) &&
      ['pending', 'unknown'].includes(item.status),
  )

  function updateHistory(
    id: string,
    patch: Partial<Transaction>,
    initial?: Transaction,
  ) {
    const previous = journal.current
    const next = previous.some((item) => item.id === id)
      ? previous.map((item) => (item.id === id ? { ...item, ...patch } : item))
      : initial
        ? [initial, ...previous].slice(0, 30)
        : previous
    journal.current = next
    setHistory(next)
    try {
      localStorage.setItem(STORAGE, JSON.stringify(next))
    } catch {
      /* Receipts remain visible in this session. */
    }
  }

  async function refresh(tokenValue = token, targetValue = target) {
    const generation = ++readGeneration.current
    setLoading(true)
    try {
      let recipient: Address | undefined
      try {
        recipient = address(targetValue)
      } catch {
        /* The address field may still be incomplete. */
      }
      const next = await readState(address(tokenValue), recipient)
      if (generation === readGeneration.current) {
        setState(next)
        setError('')
      }
    } catch (e) {
      if (generation === readGeneration.current) {
        setState(undefined)
        setError(message(e))
      }
    } finally {
      if (generation === readGeneration.current) setLoading(false)
    }
  }

  useEffect(() => {
    setState(undefined)
    setReview(undefined)
    const timer = setTimeout(() => {
      if (token) void refresh(token, target)
    }, 350)
    return () => {
      clearTimeout(timer)
      readGeneration.current++
    }
    // Refresh is scoped to the current address pair and rejects older read results.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, target])

  useEffect(() => {
    const wallet = window.ethereum
    if (!wallet) return
    let disposed = false
    const sync = () => {
      setReview(undefined)
      void walletState(wallet)
        .then((value) => {
          if (!disposed) {
            setAccount(value.account)
            setChain(value.chain)
          }
        })
        .catch(() => {
          if (!disposed) {
            setAccount(undefined)
            setChain(undefined)
          }
        })
    }
    sync()
    wallet.on?.('accountsChanged', sync)
    wallet.on?.('chainChanged', sync)
    wallet.on?.('disconnect', sync)
    return () => {
      disposed = true
      wallet.removeListener?.('accountsChanged', sync)
      wallet.removeListener?.('chainChanged', sync)
      wallet.removeListener?.('disconnect', sync)
    }
  }, [])

  async function connect() {
    if (!window.ethereum) {
      setError('请在安装了 EVM 钱包扩展的浏览器中打开控制台。')
      return
    }
    setBusy('连接钱包')
    try {
      await window.ethereum.request({ method: 'eth_requestAccounts' })
      const value = await walletState(window.ethereum)
      setAccount(value.account)
      setChain(value.chain)
      setError('')
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy('')
    }
  }

  async function switchChain() {
    if (!window.ethereum) return
    setBusy('切换网络')
    try {
      await switchNetwork(window.ethereum)
      const value = await walletState(window.ethereum)
      setAccount(value.account)
      setChain(value.chain)
      setError('')
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy('')
    }
  }

  async function prepare(event: FormEvent) {
    event.preventDefault()
    if (locked.current || unsettled) return
    locked.current = true
    setBusy('校验操作')
    setError('')
    setNotice('')
    try {
      if (!account || !window.ethereum) throw new Error('请先连接管理员钱包。')
      const next = await readState(address(token), address(target))
      const quantity =
        mode === 'clearBalance' ? next.balance! : parseAmount(amount)
      if (quantity <= 0n) throw new Error('目标地址余额为零，无需清空。')
      const snapshot: Review = {
        mode,
        token: next.token,
        target: next.target!,
        account,
        amount: quantity,
        balance: next.balance!,
        block: next.block,
      }
      await validateReview(snapshot, window.ethereum)
      setState(next)
      setReview(snapshot)
    } catch (e) {
      setError(message(e))
    } finally {
      locked.current = false
      setBusy('')
    }
  }

  async function reconcile(item: Transaction) {
    if (!item.hash) return
    try {
      const receipt = await publicClient.getTransactionReceipt({
        hash: item.hash,
      })
      const success = receiptMatches(item, receipt)
      updateHistory(item.id, {
        status: success ? 'success' : 'failed',
        block: receipt.blockNumber.toString(),
        message: success ? '链上已确认' : '交易撤销、失败或回执与操作不一致',
      })
      if (success) void refresh()
    } catch {
      /* A missing receipt is pending, never permission to resubmit. */
    }
  }

  useEffect(() => {
    const pending = history.filter(
      (item) => item.status === 'pending' && item.hash,
    )
    if (!pending.length) return
    let checking = false
    const poll = async () => {
      if (checking || document.hidden) return
      checking = true
      try {
        await Promise.all(pending.map(reconcile))
      } finally {
        checking = false
      }
    }
    void poll()
    const timer = setInterval(() => void poll(), 5000)
    return () => clearInterval(timer)
    // Each persisted hash is checked; receipt checks never send a transaction.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history])

  async function execute() {
    if (!review || !window.ethereum || locked.current || unsettled) return
    locked.current = true
    setBusy('请在钱包中确认')
    setError('')
    const item: Transaction = {
      id: crypto.randomUUID(),
      mode: review.mode,
      token: review.token,
      target: review.target,
      account: review.account,
      amount: review.amount.toString(),
      at: new Date().toISOString(),
      status: 'pending',
    }
    let submitted = false
    try {
      const hash = await submit(review, window.ethereum)
      item.hash = hash
      submitted = true
      updateHistory(item.id, {}, item)
      setReview(undefined)
      setBusy('等待链上回执')
      const receipt = await publicClient.waitForTransactionReceipt({
        hash,
        timeout: 60000,
        onReplaced: (replacement) => {
          item.hash = replacement.transaction.hash
          updateHistory(item.id, { hash: item.hash })
        },
      })
      const success = receiptMatches(item, receipt)
      updateHistory(item.id, {
        status: success ? 'success' : 'failed',
        block: receipt.blockNumber.toString(),
        message: success ? '链上已确认' : '交易失败或已取消',
      })
      setNotice(
        success
          ? `${modeName(item.mode)}成功，已确认 ${formatAmount(BigInt(item.amount))} kUSDC。`
          : '该操作未成功，请查看交易回执。',
      )
      await refresh()
    } catch (e) {
      if (submitted) {
        setNotice(
          '交易已提交，正在继续查询回执。请勿重复操作；刷新页面也会恢复查询。',
        )
      } else if (
        e instanceof SubmissionError &&
        !/已取消|rejected|denied/i.test(e.message)
      ) {
        updateHistory(
          item.id,
          {},
          {
            ...item,
            status: 'unknown',
            message: '尚未获得交易 Hash，请先检查钱包交易记录。',
          },
        )
        setReview(undefined)
        setError(e.message)
      } else {
        setError(message(e))
        setReview(undefined)
      }
    } finally {
      locked.current = false
      setBusy('')
    }
  }

  const disabled = Boolean(
    busy ||
    loading ||
    !state ||
    !owner ||
    !networkOK ||
    state.paused ||
    state.ownerBlocked ||
    unsettled,
  )
  return (
    <div className="app">
      <header className="header">
        <a className="brand" href="#">
          <span className="brand-icon">k</span>
          <span>
            kUSDC <small>控制台</small>
          </span>
        </a>
        <div className="connection">
          <span className="network">
            <i /> Monad Testnet
          </span>
          <button
            className="wallet-button"
            onClick={() => void connect()}
            disabled={Boolean(busy)}
          >
            {account ? short(account) : '连接钱包'} <span>↗</span>
          </button>
        </div>
      </header>

      <main>
        <div className="page-heading">
          <div>
            <span className="eyebrow">TEST TOKEN OPERATIONS</span>
            <h1>测试币，随手管理。</h1>
            <p>发放、没收、清空。每次变更都在 Monad Testnet 上完成。</p>
          </div>
          <span className="test-badge">
            TESTNET ONLY <span>01</span>
          </span>
        </div>

        <section className="overview" aria-label="合约信息">
          <div>
            <span className="label">代币供应量</span>
            <strong>
              {state ? formatAmount(state.supply, true) : '—'}{' '}
              <small>kUSDC</small>
            </strong>
            <span className="meta">6 位精度 · 1 kUSDC = 1,000,000 单位</span>
          </div>
          <div>
            <span className="label">合约状态</span>
            <strong className="contract-status">
              <i className={state && !state.paused ? 'healthy' : ''} />
              {loading
                ? '读取中'
                : state
                  ? state.paused
                    ? '已暂停'
                    : '正常运行'
                  : '未连接'}
            </strong>
            <span className="meta">Monad Testnet · Chain ID 10143</span>
          </div>
          <div>
            <span className="label">合约管理员</span>
            <strong className="address">
              {state ? (
                <a
                  href={`${explorer}/address/${state.owner}`}
                  target="_blank"
                  rel="noreferrer"
                  title={state.owner}
                >
                  {short(state.owner)} ↗
                </a>
              ) : (
                '—'
              )}
            </strong>
            <span className="meta">
              {account
                ? owner
                  ? '当前钱包拥有管理权限'
                  : '当前钱包不是合约 owner'
                : '连接 owner 钱包后操作'}
            </span>
          </div>
        </section>

        <div className="workspace">
          <section
            className="operation-panel"
            aria-labelledby="operation-title"
          >
            <div className="panel-heading">
              <h2 id="operation-title">余额管理</h2>
              <span className="subtle">ON-CHAIN</span>
            </div>
            <div className="tabs" role="group" aria-label="操作类型">
              {modes.map((item) => (
                <button
                  key={item.id}
                  className={mode === item.id ? 'active' : ''}
                  aria-pressed={mode === item.id}
                  disabled={Boolean(busy)}
                  onClick={() => {
                    setMode(item.id)
                    setReview(undefined)
                    setError('')
                    setNotice('')
                  }}
                >
                  <span>{item.mark}</span>
                  {item.name}
                </button>
              ))}
            </div>
            <p className="mode-description">{current.description}</p>
            {!networkOK && account && (
              <div className="network-alert">
                钱包当前不在 Monad Testnet。
                <button
                  onClick={() => void switchChain()}
                  disabled={Boolean(busy)}
                >
                  切换网络 ↗
                </button>
              </div>
            )}
            <form onSubmit={(event) => void prepare(event)}>
              <label htmlFor="target">
                目标地址 <span>RECIPIENT</span>
              </label>
              <input
                id="target"
                value={target}
                onChange={(event) => setTarget(event.target.value.trim())}
                placeholder="0x…"
                spellCheck={false}
                autoComplete="off"
                disabled={Boolean(busy)}
                required
              />
              <div className="balance-row">
                <span>当前余额</span>
                <strong>
                  {state?.balance !== undefined
                    ? formatAmount(state.balance, true)
                    : '—'}{' '}
                  <small>kUSDC</small>
                </strong>
                <button
                  type="button"
                  onClick={() => void refresh()}
                  disabled={loading || Boolean(busy)}
                  aria-label="刷新余额"
                >
                  ↻
                </button>
              </div>
              <label htmlFor="amount">
                {mode === 'clearBalance' ? '将清空的数量' : '操作数量'}{' '}
                <span>AMOUNT</span>
              </label>
              <div className="amount-field">
                <input
                  id="amount"
                  inputMode="decimal"
                  value={
                    mode === 'clearBalance'
                      ? state?.balance !== undefined
                        ? formatAmount(state.balance)
                        : ''
                      : amount
                  }
                  onChange={(event) => {
                    setAmount(event.target.value)
                    setReview(undefined)
                  }}
                  placeholder="0.00"
                  readOnly={mode === 'clearBalance'}
                  disabled={Boolean(busy)}
                  required
                />
                <span>kUSDC</span>
              </div>
              <p className="field-note">
                {mode === 'clearBalance'
                  ? '清空全部余额；余额变化后需重新确认。'
                  : '最多 6 位小数，例如 0.0042。'}
              </p>
              {state?.targetBlocked && (
                <p className="error">该地址处于代币黑名单中，暂时无法操作。</p>
              )}
              {!review && (
                <button
                  className={`primary ${mode === 'mint' ? '' : 'destructive'}`}
                  type="submit"
                  disabled={disabled || state?.targetBlocked}
                >
                  {busy || `预览${current.name}操作`} <span>→</span>
                </button>
              )}
            </form>
            {review && (
              <div className="review" role="region" aria-label="操作预览">
                <div className="review-title">
                  <strong>确认{modeName(review.mode)}</strong>
                  <span>#{review.block.toString()}</span>
                </div>
                <p className="review-amount">
                  {review.mode === 'mint' ? '+' : '−'}
                  {formatAmount(review.amount)} <small>kUSDC</small>
                </p>
                <dl>
                  <div>
                    <dt>目标地址</dt>
                    <dd>{review.target}</dd>
                  </div>
                  <div>
                    <dt>余额变化</dt>
                    <dd>
                      {formatAmount(review.balance)} →{' '}
                      {formatAmount(
                        review.mode === 'mint'
                          ? review.balance + review.amount
                          : review.balance - review.amount,
                      )}
                    </dd>
                  </div>
                  <div>
                    <dt>网络</dt>
                    <dd>Monad Testnet · 10143</dd>
                  </div>
                </dl>
                <div className="review-actions">
                  <button
                    onClick={() => setReview(undefined)}
                    disabled={Boolean(busy)}
                  >
                    返回修改
                  </button>
                  <button
                    className={`primary ${review.mode === 'mint' ? '' : 'destructive'}`}
                    onClick={() => void execute()}
                    disabled={Boolean(busy) || unsettled}
                  >
                    {busy || '在钱包中确认'} ↗
                  </button>
                </div>
              </div>
            )}
            <div aria-live="polite">
              {error && (
                <p className="feedback error" role="alert">
                  {error}
                </p>
              )}
              {notice && <p className="feedback notice">{notice}</p>}
              {unsettled && !busy && (
                <p className="feedback notice">
                  有一笔操作等待确认，请先查看下方交易记录。
                </p>
              )}
            </div>
          </section>

          <aside className="side-panel">
            <div className="token-emblem">
              <span>k</span>
              <i>USDC</i>
              <b>06</b>
            </div>
            <h2>仅用于测试。</h2>
            <p>这是一枚 Monad Testnet 测试代币，由合约管理员管理供应量。</p>
            <dl className="facts">
              <div>
                <dt>代币符号</dt>
                <dd>kUSDC</dd>
              </div>
              <div>
                <dt>网络 Gas</dt>
                <dd>Test MON</dd>
              </div>
              <div>
                <dt>签名方式</dt>
                <dd>浏览器钱包</dd>
              </div>
              <div>
                <dt>扣减方式</dt>
                <dd>直接销毁</dd>
              </div>
            </dl>
            <details className="configuration">
              <summary>
                合约设置 <span>↗</span>
              </summary>
              <label htmlFor="token">管理版 kUSDC 地址</label>
              <input
                id="token"
                value={token}
                onChange={(event) => setToken(event.target.value.trim())}
                placeholder="0x…"
                spellCheck={false}
                disabled={Boolean(busy)}
              />
              <p>
                仅接受新版 kUSDC 管理接口。发放、没收和清空都需要 owner 钱包。
              </p>
            </details>
            {state && (
              <a
                className="explorer-link"
                href={`${explorer}/address/${state.token}`}
                target="_blank"
                rel="noreferrer"
              >
                在浏览器查看合约 ↗
              </a>
            )}
          </aside>
        </div>

        <section className="transactions" aria-labelledby="transactions-title">
          <div className="panel-heading">
            <h2 id="transactions-title">操作记录</h2>
            <span className="subtle">保存在本浏览器 · 最近 30 笔</span>
          </div>
          {history.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>操作 / 时间</th>
                    <th>目标地址</th>
                    <th>数量</th>
                    <th>状态</th>
                    <th>交易</th>
                  </tr>
                </thead>
                <tbody>
                  {history.map((item) => (
                    <tr key={item.id}>
                      <td>
                        <strong>{modeName(item.mode)}</strong>
                        <small>
                          {new Date(item.at).toLocaleString('zh-CN')}
                        </small>
                      </td>
                      <td>
                        <code title={item.target}>{short(item.target)}</code>
                      </td>
                      <td>
                        {formatAmount(BigInt(item.amount), true)}{' '}
                        <small>kUSDC</small>
                      </td>
                      <td>
                        <span className={`status ${item.status}`}>
                          {
                            {
                              pending: '等待确认',
                              success: '已确认',
                              failed: '未成功',
                              unknown: '请检查钱包',
                            }[item.status]
                          }
                        </span>
                        {item.message && (
                          <small className="record-message">
                            {item.message}
                          </small>
                        )}
                      </td>
                      <td>
                        {item.hash ? (
                          <>
                            <a
                              href={`${explorer}/tx/${item.hash}`}
                              target="_blank"
                              rel="noreferrer"
                            >
                              {short(item.hash)} ↗
                            </a>
                            {item.status === 'pending' && (
                              <button
                                className="text-button"
                                onClick={() => void reconcile(item)}
                              >
                                检查回执
                              </button>
                            )}
                          </>
                        ) : item.status === 'unknown' ? (
                          <button
                            className="text-button"
                            onClick={() => {
                              if (
                                window.confirm(
                                  '请先在钱包中确认这笔操作没有提交。已有交易时应等待其完成，避免重复操作。确认没有提交后才解除锁定。',
                                )
                              )
                                updateHistory(item.id, {
                                  status: 'failed',
                                  message: '已人工核对钱包，解除未知提交状态',
                                })
                            }}
                          >
                            核对钱包后解除锁定
                          </button>
                        ) : (
                          '—'
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="empty">
              <span>↗</span>
              <p>还没有操作记录</p>
              <small>提交后的交易 Hash 和链上确认结果会显示在这里。</small>
            </div>
          )}
        </section>
      </main>
      <footer>
        <span>
          kUSDC CONSOLE <b>·</b> STANDALONE
        </span>
        <span>Monad Testnet · 10143</span>
      </footer>
    </div>
  )
}
