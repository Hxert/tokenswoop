import type { ReactNode } from 'react'
import { ArrowUpRight, Check, Copy, Inbox, LoaderCircle, X } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { network } from './network'
import { homeHref } from './siteRouting'
import SwoopMark from './SwoopMark'

export function Brand() {
  return (
    <div className="c-brand-block">
      <a className="c-brand" href={homeHref(window.location.hostname)}>
        <SwoopMark />
        TokenSwoop
      </a>
      {network().testnet && <span className="network-badge">TestNet</span>}
    </div>
  )
}
export function PageTitle({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string
  title: string
  description: string
  action?: ReactNode
}) {
  return (
    <div className="c-page-title">
      <div>
        {eyebrow && <span className="c-eyebrow">{eyebrow}</span>}
        <h1>
          {title}
          <span>.</span>
        </h1>
        <p>{description}</p>
      </div>
      {action}
    </div>
  )
}
export function Notice({
  children,
  error = false,
}: {
  children: ReactNode
  error?: boolean
}) {
  return (
    <div
      className={`c-notice ${error ? 'is-error' : ''}`}
      role={error ? 'alert' : 'status'}
    >
      {children}
    </div>
  )
}
export function Empty({
  title,
  children,
  action,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="c-empty">
      <div className="c-empty-icon">
        <Inbox size={25} strokeWidth={1.3} />
      </div>
      <h3>{title}</h3>
      <p>{children}</p>
      {action}
    </div>
  )
}
export function Loading() {
  return (
    <div className="c-loading" role="status">
      <LoaderCircle className="c-spin" size={20} /> Loading your workspace…
    </div>
  )
}
export function State({ loading, error }: { loading: boolean; error: string }) {
  return loading ? <Loading /> : error ? <Notice error>{error}</Notice> : null
}
export function Badge({
  children,
  tone = '',
}: {
  children: ReactNode
  tone?: string
}) {
  return <span className={`c-badge ${tone}`}>{children}</span>
}
export function CopyButton({
  value,
  label = 'Copy',
}: {
  value: string
  label?: string
}) {
  const [copied, setCopied] = useState(false),
    [error, setError] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const feedback = copied
    ? 'Copied'
    : error
      ? 'Select and copy manually'
      : label
  return (
    <button
      type="button"
      className={`c-copy-icon${copied ? ' is-copied' : ''}`}
      aria-label={feedback}
      title={feedback}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
          setError(false)
          clearTimeout(timer.current)
          timer.current = setTimeout(() => setCopied(false), 2000)
        } catch {
          setError(true)
        }
      }}
    >
      {copied ? (
        <Check size={15} aria-hidden="true" />
      ) : (
        <Copy size={15} aria-hidden="true" />
      )}
      <span className="sr-only" role="status">
        {copied || error ? feedback : ''}
      </span>
    </button>
  )
}
export function CopyValue({
  value,
  label,
  className = '',
}: {
  value: string
  label: string
  className?: string
}) {
  return (
    <span className={`c-copy-value ${className}`}>
      <code>{value}</code>
      <CopyButton value={value} label={label} />
    </span>
  )
}

export function Modal({
  title,
  children,
  onClose,
  busy = false,
  wide = false,
}: {
  title: string
  children: ReactNode
  onClose: () => void
  busy?: boolean
  wide?: boolean
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  useEffect(() => {
    const element = dialog.current!
    const overflow = document.body.style.overflow
    const opener =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null
    element.showModal()
    element
      .querySelector<HTMLElement>('[data-autofocus]')
      ?.focus({ preventScroll: true })
    document.body.style.overflow = 'hidden'
    return () => {
      element.close()
      document.body.style.overflow = overflow
      if (opener?.isConnected) opener.focus({ preventScroll: true })
    }
  }, [])
  return (
    <dialog
      ref={dialog}
      className={`c-dialog${wide ? ' c-dialog-wide' : ''}`}
      aria-labelledby={titleID}
      onCancel={(event) => {
        event.preventDefault()
        if (!busy) onClose()
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget || busy) return
        const rect = event.currentTarget.getBoundingClientRect()
        if (
          event.clientX < rect.left ||
          event.clientX > rect.right ||
          event.clientY < rect.top ||
          event.clientY > rect.bottom
        )
          onClose()
      }}
    >
      <div className="c-dialog-header">
        <h2 id={titleID}>{title}</h2>
        <button
          type="button"
          className="c-icon"
          aria-label={`Close ${title}`}
          onClick={onClose}
          disabled={busy}
        >
          <X size={20} />
        </button>
      </div>
      <div className="c-dialog-body">{children}</div>
    </dialog>
  )
}
export function Go({ to, children }: { to: string; children: ReactNode }) {
  return (
    <a className="c-button c-primary" href={`#${to}`}>
      {children}
      <ArrowUpRight size={16} />
    </a>
  )
}
