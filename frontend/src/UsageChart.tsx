import { useId, useState } from 'react'
import { money } from './api'
import type { Stats } from './api'

export default function UsageChart({ daily }: { daily?: Stats['daily'] }) {
  const [activeDate, setActiveDate] = useState<string | null>(null)
  const tooltipID = useId()
  const days = Array.from({ length: 14 }, (_, i) => {
    const day = new Date()
    day.setUTCDate(day.getUTCDate() - 13 + i)
    const date = day.toISOString().slice(0, 10)
    const usage = daily?.find((row) => row.date === date)
    return {
      date,
      requests: usage?.requests ?? 0,
      spent: usage?.spent ?? '0',
      input: usage?.input_tokens ?? 0,
      output: usage?.output_tokens ?? 0,
    }
  })
  const max = Math.max(1, ...days.map((day) => day.requests))
  const activeIndex = days.findIndex((day) => day.date === activeDate)
  const active = daily ? days[activeIndex] : undefined

  return (
    <div
      className="c-chart"
      role="group"
      aria-label="Daily usage over the last 14 days, UTC"
      onMouseLeave={() => setActiveDate(null)}
      onKeyDown={(event) => {
        if (event.key === 'Escape') setActiveDate(null)
      }}
    >
      <div className="c-chart-grid" aria-hidden="true" />
      {days.map((day) => (
        <button
          type="button"
          className={`c-chart-column ${active?.date === day.date ? 'is-active' : ''}`}
          key={day.date}
          disabled={!daily}
          aria-label={`${day.date}: ${day.requests} requests`}
          aria-describedby={active?.date === day.date ? tooltipID : undefined}
          onMouseEnter={() => setActiveDate(day.date)}
          onFocus={() => setActiveDate(day.date)}
          onBlur={() => setActiveDate(null)}
          onClick={() => setActiveDate(day.date)}
        >
          <span
            className={`c-chart-bar ${day.requests === 0 ? 'zero' : ''}`}
            style={{ height: `${Math.max((day.requests / max) * 100, 2)}%` }}
            aria-hidden="true"
          />
          <span className="c-chart-date" aria-hidden="true">
            {day.date.slice(-2)}
          </span>
        </button>
      ))}
      {active && (
        <div
          id={tooltipID}
          role="tooltip"
          className="c-chart-tooltip"
          style={{
            left: `clamp(0px, calc(${((activeIndex + 0.5) / days.length) * 100}% - 112px), calc(100% - 224px))`,
          }}
        >
          <strong>{active.date} · UTC</strong>
          <dl>
            <div>
              <dt>Requests</dt>
              <dd>{active.requests.toLocaleString()}</dd>
            </div>
            <div>
              <dt>Input tokens</dt>
              <dd>{active.input.toLocaleString()}</dd>
            </div>
            <div>
              <dt>Output tokens</dt>
              <dd>{active.output.toLocaleString()}</dd>
            </div>
            <div>
              <dt>Actual cost</dt>
              <dd>${money(active.spent)}</dd>
            </div>
          </dl>
        </div>
      )}
    </div>
  )
}
