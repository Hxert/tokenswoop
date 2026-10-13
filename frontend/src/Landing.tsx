import { siteURLs } from './siteRouting'
import { useEffect, useRef, useState } from 'react'
import {
  ArrowDown,
  ArrowRight,
  ArrowUpRight,
  Blocks,
  Check,
  Menu,
  Pause,
  Play,
  Sparkles,
  Wallet,
  X,
  Zap,
} from 'lucide-react'

import AsciiLogo from './AsciiLogo'
import BrandTicker from './BrandTicker'
import SwoopMark from './SwoopMark'
import { monadLogo } from './brands'

function MonadMark() {
  return (
    <img
      className="monad-mark"
      src={monadLogo}
      width="18"
      height="18"
      alt=""
      aria-hidden="true"
    />
  )
}

const flow = [
  {
    label: 'Approve your budget',
    title: 'Your wallet. Your say.',
    description:
      'Choose how much USDC to authorize. Your funds stay in your wallet until a bill is settled.',
    icon: Wallet,
    code: '01',
    status: 'USDC allowance',
    detail: 'You control the spending limit.',
  },
  {
    label: 'Put intelligence to work',
    title: 'Same workflow. New possibilities.',
    description:
      'Build with OpenAI-compatible APIs. Let TokenSwoop handle the connection between AI usage and payment.',
    icon: Sparkles,
    code: '02',
    status: 'AI gateway',
    detail: 'One familiar interface for your ideas.',
  },
  {
    label: 'Settle what you use',
    title: 'Every bill, on-chain.',
    description:
      'Usage is grouped into bills and settled in USDC on Monad. A verifiable receipt for every payment.',
    icon: Blocks,
    code: '03',
    status: 'Monad settlement',
    detail: 'Pay for usage, as you go.',
  },
]

export default function App() {
  const [menuOpen, setMenuOpen] = useState(false)
  const [motionPaused, setMotionPaused] = useState(false)
  const [reducedMotion, setReducedMotion] = useState(false)
  const [activeStep, setActiveStep] = useState(0)
  const menuButtonRef = useRef<HTMLButtonElement>(null)
  const paused = motionPaused || reducedMotion
  const step = flow[activeStep]
  const StepIcon = step.icon

  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const update = () => setReducedMotion(media.matches)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])

  useEffect(() => {
    if (!menuOpen) return
    const close = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuOpen(false)
        menuButtonRef.current?.focus()
      }
    }
    window.addEventListener('keydown', close)
    return () => window.removeEventListener('keydown', close)
  }, [menuOpen])

  const closeMenu = () => setMenuOpen(false)

  return (
    <div className={`site ${paused ? 'motion-paused' : ''}`}>
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="header shell" id="top">
        <a
          className="brand"
          href="#top"
          aria-label="TokenSwoop home"
          onClick={closeMenu}
        >
          <span className="brand-mark">
            <SwoopMark />
          </span>
          <span className="brand-name">TokenSwoop</span>
        </a>
        <nav
          className={`navigation ${menuOpen ? 'is-open' : ''}`}
          aria-label="Main navigation"
          id="main-navigation"
        >
          <a href="#vision" onClick={closeMenu}>
            The vision <ArrowUpRight size={13} />
          </a>
          <a href="#how-it-works" onClick={closeMenu}>
            How it works <ArrowUpRight size={13} />
          </a>
          <span className="nav-divider" />
          <a
            className="chain-label"
            href={siteURLs.testnet}
            onClick={closeMenu}
          >
            <MonadMark /> TestNet <ArrowUpRight size={13} />
          </a>
        </nav>
        <a className="launch-pill" href={siteURLs.mainnet}>
          <span className="status-dot" /> Open App <ArrowUpRight size={15} />
        </a>
        <button
          ref={menuButtonRef}
          className="menu-toggle"
          type="button"
          onClick={() => setMenuOpen(!menuOpen)}
          aria-expanded={menuOpen}
          aria-controls="main-navigation"
          aria-label={menuOpen ? 'Close menu' : 'Open menu'}
        >
          {menuOpen ? <X size={22} /> : <Menu size={22} />}
        </button>
      </header>

      <main id="main">
        <section className="hero shell" aria-labelledby="hero-title">
          <div className="hero-copy">
            <div className="eyebrow">
              <span className="crosshair" /> A NEW ORBIT FOR AI{' '}
              <span className="eyebrow-line" />
            </div>
            <h1 id="hero-title">
              Intelligence, <br />
              in your{' '}
              <span className="orbit-word">
                orbit
                <svg viewBox="0 0 295 24" fill="none" aria-hidden="true">
                  <path
                    d="M3 14C63 2 172 0 289 8M25 21c83-8 171-9 251-6"
                    stroke="currentColor"
                    strokeWidth="3"
                    strokeLinecap="round"
                  />
                </svg>
              </span>
              <span className="hero-period">.</span>
            </h1>
            <p className="hero-description">
              Powerful AI. Pay-as-you-go. <br />
              USDC in your wallet. Settlement on Monad.
            </p>
            <div className="hero-actions">
              <a className="primary-button" href={siteURLs.mainnet}>
                Enter your workspace <ArrowUpRight size={18} />
              </a>
              <span className="hero-note">
                LESS FRICTION.
                <br />
                MORE POSSIBILITIES.
              </span>
            </div>
            <div className="hero-tags">
              <span>
                <span className="tiny-dot" /> USDC PAYMENTS
              </span>
              <span>OPENAI-COMPATIBLE</span>
            </div>
          </div>

          <figure
            className="hero-art"
            aria-label="Binary digits 0 and 1 forming USDC, OpenAI, Anthropic, Gemini and Monad logos"
          >
            <AsciiLogo paused={paused} />
            <button
              className="motion-toggle"
              type="button"
              onClick={() => setMotionPaused(!motionPaused)}
              disabled={reducedMotion}
              aria-pressed={paused}
              aria-label={
                reducedMotion
                  ? 'Motion off: reduced motion preference'
                  : paused
                    ? 'Resume animation'
                    : 'Pause animation'
              }
            >
              {paused ? <Play size={11} /> : <Pause size={11} />}{' '}
              <span>MOTION {paused ? 'OFF' : 'ON'}</span>
            </button>
          </figure>
          <div className="hero-bottom">
            <span>BUILT FOR THE WAY YOU BUILD.</span>
            <a href="#vision">
              SCROLL TO DISCOVER <ArrowDown size={13} />
            </a>
          </div>
        </section>

        <BrandTicker />

        <section
          className="vision-section shell"
          id="vision"
          aria-labelledby="vision-title"
        >
          <div className="section-topline">
            <span className="eyebrow">
              <span className="section-index">01 /</span> THE VISION
            </span>
            <span className="section-side-note">SMARTER BY DESIGN.</span>
          </div>
          <div className="vision-heading">
            <h2 id="vision-title">
              Big ideas don't need
              <br />
              <span>monthly subscriptions.</span>
            </h2>
            <p>
              We're connecting the power of AI with the freedom of Web3.
              Familiar tools. Flexible payments. More room to create.
            </p>
          </div>
          <div className="feature-grid">
            <article className="feature">
              <div className="feature-top">
                <span>01</span>
                <Zap size={25} strokeWidth={1.5} />
              </div>
              <h3>Every token counts.</h3>
              <p>
                Pay for the AI you actually use in USDC. No prepaid platform
                balance. No monthly commitment.
              </p>
              <span className="feature-tag">
                PAY-AS-YOU-GO <ArrowUpRight size={13} />
              </span>
            </article>
            <article className="feature">
              <div className="feature-top">
                <span>02</span>
                <Wallet size={25} strokeWidth={1.5} />
              </div>
              <h3>Your funds stay yours.</h3>
              <p>
                Keep your payment funds in your wallet. Set your USDC allowance
                and stay in control of your budget.
              </p>
              <span className="feature-tag">
                WALLET-FIRST <ArrowUpRight size={13} />
              </span>
            </article>
            <article className="feature">
              <div className="feature-top">
                <span>03</span>
                <Blocks size={25} strokeWidth={1.5} />
              </div>
              <h3>Value moves on Monad.</h3>
              <p>
                On-chain settlement connects your AI usage to verifiable
                payments. Built for a world that keeps moving.
              </p>
              <span className="feature-tag">
                MONAD NATIVE <ArrowUpRight size={13} />
              </span>
            </article>
          </div>
        </section>

        <section
          className="flow-section shell"
          id="how-it-works"
          aria-labelledby="flow-title"
        >
          <div className="flow-heading">
            <span className="eyebrow">
              <span className="section-index">02 /</span> THE FLOW
            </span>
            <h2 id="flow-title">
              From idea <br />
              to <span>intelligence.</span>
            </h2>
            <p>
              Three simple moves. <br />A whole new way forward.
            </p>
          </div>
          <div className="flow-content">
            <div
              className="flow-steps"
              role="tablist"
              aria-label="How TokenSwoop works"
            >
              {flow.map((item, i) => (
                <button
                  type="button"
                  role="tab"
                  key={item.code}
                  id={`step-${i}`}
                  aria-selected={activeStep === i}
                  aria-controls="flow-panel"
                  tabIndex={activeStep === i ? 0 : -1}
                  className={activeStep === i ? 'active' : ''}
                  onClick={() => setActiveStep(i)}
                  onKeyDown={(event) => {
                    let next: number
                    if (event.key === 'ArrowRight') next = (i + 1) % flow.length
                    else if (event.key === 'ArrowLeft')
                      next = (i - 1 + flow.length) % flow.length
                    else if (event.key === 'Home') next = 0
                    else if (event.key === 'End') next = flow.length - 1
                    else return
                    event.preventDefault()
                    setActiveStep(next)
                    document.getElementById(`step-${next}`)?.focus()
                  }}
                >
                  <span>{item.code}</span>
                  <span>{item.label}</span>
                  <ArrowUpRight size={16} />
                </button>
              ))}
            </div>
            <div
              className="flow-panel"
              id="flow-panel"
              role="tabpanel"
              tabIndex={0}
              aria-labelledby={`step-${activeStep}`}
            >
              <div className="flow-panel-top">
                <span className="flow-panel-icon">
                  <StepIcon size={27} strokeWidth={1.5} />
                </span>
                <span className="flow-panel-label">
                  {step.status}
                  <span className="tiny-dot" />
                </span>
              </div>
              <h3>{step.title}</h3>
              <p>{step.description}</p>
              <div className="flow-panel-bottom">
                <span>
                  <Check size={14} /> {step.detail}
                </span>
                <span>{step.code} / 03</span>
              </div>
            </div>
          </div>
        </section>

        <section
          className="launch-section shell"
          id="launch"
          aria-labelledby="launch-title"
        >
          <div className="launch-banner">
            <div>
              <span className="launch-eyebrow">
                <span className="status-dot" /> NOW LIVE
              </span>
              <h2 id="launch-title">
                Your next idea
                <br />
                starts <span>here.</span>
              </h2>
              <p>
                Start using AI with your wallet.
                <br />
                Pay for actual usage in USDC on Monad.
              </p>
              <a className="launch-button" href={siteURLs.mainnet}>
                Open App <ArrowUpRight size={18} />
              </a>
            </div>
            <div className="launch-graphic" aria-hidden="true">
              <div className="launch-graphic-circle" />
              <SwoopMark />
              <ArrowUpRight className="launch-arrow" />
            </div>
            <span className="launch-banner-note">AI, ON YOUR TERMS.</span>
          </div>
        </section>
      </main>

      <footer className="footer shell">
        <a
          className="brand footer-brand"
          href="#top"
          aria-label="Back to TokenSwoop home"
        >
          <span className="brand-mark">
            <SwoopMark />
          </span>
          <span className="brand-name">TokenSwoop</span>
        </a>
        <span className="footer-note">A NEW ORBIT FOR AI.</span>
        <a href="#top" className="back-top">
          BACK TO TOP <ArrowRight size={15} />
        </a>
        <div className="footer-bottom">
          <span>© {new Date().getFullYear()} TokenSwoop</span>
          <span>
            <MonadMark /> BUILT ON MONAD. BUILT FOR YOU.
          </span>
          <span>
            NOW LIVE <span className="tiny-dot" />
          </span>
        </div>
      </footer>
    </div>
  )
}
