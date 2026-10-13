import { useEffect, useRef, useState } from 'react'
import { characterBrands } from './brands'

const SIZE = 500
const COLUMNS = 100
const ROWS = 64
const CELL_WIDTH = SIZE / COLUMNS
const CELL_HEIGHT = SIZE / ROWS
const CELL_COUNT = COLUMNS * ROWS
const CHARACTERS = '01'
const HOLD_MS = 5000
const MORPH_MS = 900
const HOVER_RADIUS = 85
const HOVER_PUSH = 14
const POINTER_EASE_MS = 130

async function sampleLogo(src: string): Promise<Float32Array> {
  const image = new Image()
  image.src = src
  await image.decode()
  const maskCanvas = document.createElement('canvas')
  maskCanvas.width = SIZE
  maskCanvas.height = SIZE
  const context = maskCanvas.getContext('2d', { willReadFrequently: true })
  if (!context) throw new Error('Canvas is unavailable')
  const scale = 430 / Math.max(image.naturalWidth, image.naturalHeight)
  const width = image.naturalWidth * scale
  const height = image.naturalHeight * scale
  context.drawImage(
    image,
    (SIZE - width) / 2,
    (SIZE - height) / 2,
    width,
    height,
  )
  const { data } = context.getImageData(0, 0, SIZE, SIZE)
  const mask = new Float32Array(CELL_COUNT)
  for (let i = 0; i < CELL_COUNT; i++) {
    const x = Math.floor(((i % COLUMNS) + 0.5) * CELL_WIDTH)
    const y = Math.floor((Math.floor(i / COLUMNS) + 0.5) * CELL_HEIGHT)
    let alpha = 0
    for (const offsetY of [-2, 0, 2]) {
      for (const offsetX of [-1, 0, 1]) {
        alpha += data[((y + offsetY) * SIZE + x + offsetX) * 4 + 3]
      }
    }
    mask[i] = alpha / (9 * 255) > 0.32 ? 1 : 0
  }
  return mask
}

export default function AsciiLogo({ paused }: { paused: boolean }) {
  const [active, setActive] = useState(0)
  const [masks, setMasks] = useState<Float32Array[] | null>(null)
  const [failed, setFailed] = useState(false)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const stageRef = useRef<HTMLDivElement>(null)
  const activeRef = useRef(active)
  const pausedRef = useRef(paused)
  const visibleRef = useRef(true)
  const redrawRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    activeRef.current = active
    redrawRef.current?.()
  }, [active])

  useEffect(() => {
    pausedRef.current = paused
  }, [paused])

  useEffect(() => {
    let cancelled = false
    Promise.all(characterBrands.map((brand) => sampleLogo(brand.src)))
      .then((loaded) => {
        if (!cancelled) setMasks(loaded)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    const observer = new IntersectionObserver(([entry]) => {
      visibleRef.current = entry.isIntersecting
    })
    observer.observe(stage)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (paused || !masks) return
    const timer = window.setInterval(() => {
      if (visibleRef.current && !document.hidden) {
        setActive((index) => (index + 1) % characterBrands.length)
      }
    }, HOLD_MS)
    return () => window.clearInterval(timer)
  }, [active, paused, masks])

  useEffect(() => {
    const canvas = canvasRef.current
    const stage = stageRef.current
    if (!canvas || !stage || !masks) return
    const context = canvas.getContext('2d')
    if (!context) {
      setFailed(true)
      return
    }

    const characters = Array.from(
      { length: CELL_COUNT },
      () => CHARACTERS[Math.floor(Math.random() * CHARACTERS.length)],
    )
    const noise = Float32Array.from({ length: CELL_COUNT }, () => Math.random())
    const directions = Float32Array.from(
      { length: CELL_COUNT },
      () => Math.random() * Math.PI * 2,
    )
    let selected = activeRef.current
    let from = masks[selected].slice()
    let weights = from.slice()
    let progressTime = MORPH_MS
    let frame = 0
    let lastTime = 0
    let lastPaint = 0
    let accumulated = 0
    let disposed = false
    const hoverMedia = window.matchMedia('(hover: hover) and (pointer: fine)')
    const pointer = {
      x: SIZE / 2,
      y: SIZE / 2,
      targetX: SIZE / 2,
      targetY: SIZE / 2,
      strength: 0,
      targetStrength: 0,
    }

    const movePointer = (event: PointerEvent) => {
      if (
        event.pointerType !== 'mouse' ||
        !hoverMedia.matches ||
        pausedRef.current
      ) {
        return
      }
      const bounds = canvas.getBoundingClientRect()
      if (!bounds.width || !bounds.height) return
      pointer.targetX = ((event.clientX - bounds.left) / bounds.width) * SIZE
      pointer.targetY = ((event.clientY - bounds.top) / bounds.height) * SIZE
      if (pointer.strength < 0.001) {
        pointer.x = pointer.targetX
        pointer.y = pointer.targetY
      }
      pointer.targetStrength = 1
    }
    const resetPointer = () => {
      pointer.targetStrength = 0
    }

    const paint = (scatter = 0) => {
      const scale = canvas.width / SIZE
      context.setTransform(scale, 0, 0, scale, 0, 0)
      context.clearRect(0, 0, SIZE, SIZE)
      context.font = '8.5px "DM Mono", monospace'
      context.textAlign = 'center'
      context.textBaseline = 'middle'
      const parallaxX = ((pointer.x - SIZE / 2) / SIZE) * 14 * pointer.strength
      const parallaxY = ((pointer.y - SIZE / 2) / SIZE) * 14 * pointer.strength
      for (let i = 0; i < CELL_COUNT; i++) {
        const weight = weights[i]
        if (weight < 0.015) continue
        const spread = scatter * (14 + noise[i] * 42)
        let x =
          ((i % COLUMNS) + 0.5) * CELL_WIDTH +
          Math.cos(directions[i]) * spread +
          parallaxX
        let y =
          (Math.floor(i / COLUMNS) + 0.5) * CELL_HEIGHT +
          Math.sin(directions[i]) * spread +
          parallaxY
        const dx = x - pointer.x
        const dy = y - pointer.y
        const distance = Math.hypot(dx, dy)
        const proximity =
          Math.max(0, 1 - distance / HOVER_RADIUS) ** 2 * pointer.strength
        const push = proximity * HOVER_PUSH
        if (distance > 0.001) {
          x += (dx / distance) * push
          y += (dy / distance) * push
        }
        context.globalAlpha = Math.min(
          1,
          weight * (0.78 + noise[i] * 0.22 + proximity * 0.3),
        )
        context.fillStyle =
          proximity > 0.08
            ? '#f2ffe2'
            : noise[i] > 0.87
              ? '#edf9dc'
              : noise[i] > 0.3
                ? '#c6f66b'
                : '#99c557'
        context.fillText(characters[i], x, y)
      }
      context.globalAlpha = 1
      canvas.dataset.characterCount = String(
        weights.filter((value) => value > 0.5).length,
      )
      canvas.dataset.logo = characterBrands[selected].id
      canvas.dataset.phase = progressTime >= MORPH_MS ? 'settled' : 'morphing'
    }

    const advance = (delta: number) => {
      const ease = 1 - Math.exp(-delta / POINTER_EASE_MS)
      pointer.x += (pointer.targetX - pointer.x) * ease
      pointer.y += (pointer.targetY - pointer.y) * ease
      pointer.strength += (pointer.targetStrength - pointer.strength) * ease
      if (pointer.targetStrength === 0 && pointer.strength < 0.001) {
        pointer.strength = 0
      }
      if (selected !== activeRef.current) {
        selected = activeRef.current
        from = weights.slice()
        progressTime = pausedRef.current ? MORPH_MS : 0
      }
      progressTime = Math.min(MORPH_MS, progressTime + delta)
      const linear = progressTime / MORPH_MS
      const progress = linear * linear * (3 - 2 * linear)
      const scatter = Math.sin(linear * Math.PI)
      for (let i = 0; i < CELL_COUNT; i++) {
        weights[i] = from[i] + (masks[selected][i] - from[i]) * progress
        if (
          !pausedRef.current &&
          Math.random() < (scatter > 0.05 ? 0.15 : 0.025)
        ) {
          characters[i] =
            CHARACTERS[Math.floor(Math.random() * CHARACTERS.length)]
        }
      }
      paint(scatter)
    }

    const resize = () => {
      const size = canvas.getBoundingClientRect().width
      const ratio = Math.min(window.devicePixelRatio || 1, 2)
      canvas.width = Math.round(size * ratio)
      canvas.height = canvas.width
      paint(Math.sin((progressTime / MORPH_MS) * Math.PI))
    }
    const resizeObserver = new ResizeObserver(resize)
    resizeObserver.observe(canvas)
    redrawRef.current = () => advance(0)

    const animate = (time: number) => {
      frame = requestAnimationFrame(animate)
      const delta = lastTime ? Math.min(time - lastTime, 100) : 0
      lastTime = time
      if (pausedRef.current || !visibleRef.current || document.hidden) return
      accumulated += delta
      const interacting = pointer.targetStrength > 0 || pointer.strength > 0
      if (time - lastPaint < 1000 / (interacting ? 30 : 16)) return
      advance(accumulated)
      lastPaint = time
      accumulated = 0
    }

    stage.addEventListener('pointermove', movePointer)
    stage.addEventListener('pointerleave', resetPointer)
    stage.addEventListener('pointercancel', resetPointer)
    hoverMedia.addEventListener('change', resetPointer)
    window.addEventListener('blur', resetPointer)
    resize()
    frame = requestAnimationFrame(animate)
    void document.fonts.ready.then(() => {
      if (!disposed) advance(0)
    })
    return () => {
      disposed = true
      cancelAnimationFrame(frame)
      resizeObserver.disconnect()
      stage.removeEventListener('pointermove', movePointer)
      stage.removeEventListener('pointerleave', resetPointer)
      stage.removeEventListener('pointercancel', resetPointer)
      hoverMedia.removeEventListener('change', resetPointer)
      window.removeEventListener('blur', resetPointer)
      redrawRef.current = null
    }
  }, [masks])

  const brand = characterBrands[active]
  return (
    <div
      className="ascii-visual"
      data-logo={brand.id}
      data-ready={Boolean(masks) && !failed}
    >
      <div className="ascii-topline">
        <span>
          <span className="tiny-dot" /> FROM PAYMENT TO INTELLIGENCE
        </span>
        <span>
          {String(active + 1).padStart(2, '0')} /{' '}
          {String(characterBrands.length).padStart(2, '0')}
        </span>
      </div>
      <div className="ascii-stage" ref={stageRef}>
        <canvas className="ascii-canvas" ref={canvasRef} aria-hidden="true" />
        {(!masks || failed) && (
          <img
            className="ascii-fallback"
            src={brand.src}
            alt=""
            aria-hidden="true"
          />
        )}
      </div>
      <div className="ascii-caption">
        <span className="ascii-brand-name">
          <img
            src={brand.src}
            width="19"
            height="19"
            alt=""
            aria-hidden="true"
          />
          {brand.name}
        </span>
        <span>BUILT FROM 0 + 1</span>
      </div>
      <div
        className="ascii-selector"
        role="group"
        aria-label="Choose binary logo"
      >
        {characterBrands.map((item, index) => (
          <button
            key={item.id}
            type="button"
            aria-label={`Show ${item.name} binary logo`}
            aria-pressed={active === index}
            onClick={() => setActive(index)}
          >
            <span />
            {item.name}
          </button>
        ))}
      </div>
    </div>
  )
}
