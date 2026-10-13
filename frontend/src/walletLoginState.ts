export type LoginSource = 'external' | 'privy' | 'mera'
export type WalletLogin = {
  source: LoginSource
  wallet: string
  revision: string
}
export const walletLoginKey = 'tokenswoop:wallet-login:v1'
export const walletLoginEvent = 'tokenswoop:wallet-session'
const intentKey = 'tokenswoop:login-intent'
let fallback: WalletLogin | null = null
let memoryOnly = false
let loginIntent = false

// These are UI routing hints only. The server always verifies wallet ownership.
export function readWalletLogin(): WalletLogin | null {
  if (memoryOnly) return fallback
  try {
    const value = JSON.parse(localStorage.getItem(walletLoginKey) || 'null')
    return value &&
      ['external', 'privy', 'mera'].includes(value.source) &&
      /^0x[0-9a-fA-F]{40}$/.test(value.wallet)
      ? value
      : null
  } catch {
    return fallback
  }
}
export function setWalletLogin(source: LoginSource, wallet: string) {
  fallback = {
    source,
    wallet: wallet.toLowerCase(),
    revision: crypto.randomUUID(),
  }
  try {
    localStorage.setItem(walletLoginKey, JSON.stringify(fallback))
    memoryOnly = false
  } catch {
    memoryOnly = true
  }
  setLoginIntent(false)
  window.dispatchEvent(new Event(walletLoginEvent))
}
export function clearWalletLogin() {
  fallback = null
  try {
    localStorage.removeItem(walletLoginKey)
    memoryOnly = false
  } catch {
    memoryOnly = true
  }
  setLoginIntent(false)
  window.dispatchEvent(new Event(walletLoginEvent))
}
export function setLoginIntent(privy: boolean) {
  loginIntent = privy
  try {
    if (privy) sessionStorage.setItem(intentKey, 'privy')
    else sessionStorage.removeItem(intentKey)
  } catch {
    /* Redirect recovery is optional when storage is unavailable. */
  }
}
export function hasPrivyLoginIntent() {
  try {
    return sessionStorage.getItem(intentKey) === 'privy'
  } catch {
    return loginIntent
  }
}
