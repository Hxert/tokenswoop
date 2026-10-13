import { useEffect, useRef } from 'react'

type TurnstileAPI = NonNullable<Window['turnstile']>
let loading: Promise<TurnstileAPI> | undefined
function load() {
  if (window.turnstile) return Promise.resolve(window.turnstile)
  if (loading) return loading
  loading = new Promise<TurnstileAPI>((resolve, reject) => {
    const script = document.createElement('script')
    script.src =
      'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    script.async = true
    const fail = () => {
      clearTimeout(timer)
      script.remove()
      loading = undefined
      reject(new Error('Verification could not load. Please try again.'))
    }
    const timer = setTimeout(fail, 20000)
    script.onerror = fail
    script.onload = () => {
      clearTimeout(timer)
      if (window.turnstile) resolve(window.turnstile)
      else fail()
    }
    document.head.appendChild(script)
  })
  return loading
}
export default function Turnstile({
  siteKey,
  claimID,
  attempt,
  onToken,
  onError,
}: {
  siteKey: string
  claimID: string
  attempt: number
  onToken: (token: string) => void
  onError: (message: string) => void
}) {
  const container = useRef<HTMLDivElement>(null)
  const callbacks = useRef({ onToken, onError })
  callbacks.current = { onToken, onError }
  const theme =
    document.documentElement.dataset.workspaceTheme === 'light'
      ? 'light'
      : 'dark'
  useEffect(() => {
    let disposed = false,
      widget: string | undefined
    callbacks.current.onToken('')
    void load()
      .then((api) => {
        if (disposed || !container.current) return
        widget =
          api.render(container.current, {
            sitekey: siteKey,
            action: 'kusdc_faucet',
            cData: claimID,
            theme,
            size: 'flexible',
            callback: (token: string) => {
              if (!disposed) {
                callbacks.current.onError('')
                callbacks.current.onToken(token)
              }
            },
            'expired-callback': () => {
              if (!disposed) {
                callbacks.current.onToken('')
                if (widget) api.reset(widget)
              }
            },
            'error-callback': () => {
              if (!disposed) {
                callbacks.current.onToken('')
                callbacks.current.onError(
                  'Verification failed. Please try again.',
                )
              }
            },
            'timeout-callback': () => {
              if (!disposed) {
                callbacks.current.onToken('')
                callbacks.current.onError(
                  'Verification timed out. Please try again.',
                )
              }
            },
          }) ?? undefined
        if (!widget)
          throw new Error('Verification could not load. Please try again.')
      })
      .catch((e) => {
        if (!disposed) callbacks.current.onError((e as Error).message)
      })
    return () => {
      disposed = true
      if (widget) window.turnstile?.remove(widget)
    }
  }, [siteKey, claimID, attempt, theme])
  return (
    <div
      ref={container}
      className="c-turnstile"
      aria-label="Human verification"
    />
  )
}
