import { useEffect, useId, useRef, useState } from 'react'
import type { MetricsBucket } from '../api/types'
import { bands, niceMax, segments } from '../lib/chart'

interface Props {
  buckets: MetricsBucket[]
  bucketSeconds: number
  value: (b: MetricsBucket) => number | null
  format: (v: number) => string
  label: string
  height?: number
  max?: number
  integer?: boolean
}

const stateText: Record<string, string> = {
  offline: 'server not running',
  no_data: 'no data: Playkeeper was not collecting',
  not_collected: 'before Playkeeper started collecting',
}

function tick(d: Date, spanSeconds: number) {
  return spanSeconds > 2 * 86400
    ? d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

/** Time-series chart. Missing and offline periods are shaded, never zero. */
export function Chart({ buckets, bucketSeconds, value, format, label, height = 170, max, integer }: Props) {
  const wrap = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(640)
  const [hover, setHover] = useState<number | null>(null)
  const [showTable, setShowTable] = useState(false)
  const hatch = useId().replace(/:/g, '')
  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width
      if (w) setWidth(Math.max(260, Math.floor(w)))
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const padL = 36
  const padR = 8
  const padT = 8
  const padB = 22
  const n = Math.max(1, buckets.length)
  const plotW = width - padL - padR
  const plotH = height - padT - padB
  const values = buckets.map((b) => (b.state === 'online' ? value(b) : null)).filter((v): v is number => v !== null)
  const top = max ?? niceMax(Math.max(1, ...values))
  const x = (i: number) => padL + (i / n) * plotW
  const cx = (i: number) => x(i + 0.5)
  const y = (v: number) => padT + plotH - (Math.min(v, top) / top) * plotH
  const segs = segments(buckets, value)
  const bnds = bands(buckets)
  const ticks = integer ? (top <= 2 ? [0, top] : [0, Math.round(top / 2), top]) : [0, top / 2, top]
  const labelsEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(plotW / 90))))
  const spanSeconds = n * bucketSeconds
  const hb = hover !== null ? buckets[hover] : undefined
  const describe = (b: MetricsBucket) => {
    const v = b.state === 'online' ? value(b) : null
    return v !== null ? format(v) : stateText[b.state] ?? b.state
  }

  function onMove(e: React.MouseEvent<SVGSVGElement>) {
    const rect = e.currentTarget.getBoundingClientRect()
    const i = Math.floor(((e.clientX - rect.left - padL) / plotW) * n)
    setHover(i >= 0 && i < n ? i : null)
  }
  function onKey(e: React.KeyboardEvent<SVGSVGElement>) {
    if (e.key === 'ArrowRight') setHover((h) => Math.min(n - 1, (h ?? -1) + 1))
    else if (e.key === 'ArrowLeft') setHover((h) => Math.max(0, (h ?? n) - 1))
    else return
    e.preventDefault()
  }

  return (
    <div className="chart" ref={wrap}>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        height={height}
        role="img"
        aria-label={`${label}. Use the left and right arrow keys to read values, or show the table.`}
        tabIndex={0}
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        onKeyDown={onKey}
        onBlur={() => setHover(null)}
      >
        <defs>
          <pattern id={hatch} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <rect width="6" height="6" fill="#fff" />
            <line x1="0" y1="0" x2="0" y2="6" stroke="#cfcfd6" strokeWidth="2" />
          </pattern>
        </defs>
        {bnds.map((b) => (
          <rect key={`${b.state}-${b.from}`} className={`band-${b.state}`} x={x(b.from)} y={padT} width={x(b.to) - x(b.from)} height={plotH} fill={b.state === 'no_data' ? `url(#${hatch})` : undefined} />
        ))}
        <g className="axis">
          {ticks.map((t) => (
            <g key={t}>
              <line className="grid-line" x1={padL} x2={width - padR} y1={y(t)} y2={y(t)} />
              <text x={padL - 6} y={y(t) + 4} textAnchor="end">
                {integer ? Math.round(t) : format(t)}
              </text>
            </g>
          ))}
          {buckets.map((b, i) =>
            i % labelsEvery === 0 ? (
              <text key={b.start} x={x(i)} y={height - 6} textAnchor="start">
                {tick(new Date(b.start), spanSeconds)}
              </text>
            ) : null,
          )}
        </g>
        {segs.map((seg) => {
          const line = seg.map((p, k) => `${k ? 'L' : 'M'}${cx(p.index).toFixed(1)},${y(p.value).toFixed(1)}`).join(' ')
          const first = seg[0]
          const last = seg[seg.length - 1]
          if (!first || !last) return null
          const area = `${line} L${cx(last.index).toFixed(1)},${y(0)} L${cx(first.index).toFixed(1)},${y(0)} Z`
          return (
            <g key={first.index}>
              <path className="area" d={area} />
              <path className="line" d={line} />
              {seg.length === 1 && <circle className="dot" cx={cx(first.index)} cy={y(first.value)} r={3} />}
            </g>
          )
        })}
        {hover !== null && <line className="cursor" x1={cx(hover)} x2={cx(hover)} y1={padT} y2={padT + plotH} />}
      </svg>
      <div className="tooltip" aria-live="polite">
        {hb ? `${new Date(hb.start).toLocaleString()}: ${describe(hb)}` : '\u00a0'}
      </div>
      <button type="button" className="btn ghost small" onClick={() => setShowTable((s) => !s)} aria-expanded={showTable}>
        {showTable ? 'Hide table' : 'Show as table'}
      </button>
      {showTable && (
        <div className="table-wrap" tabIndex={0}>
          <table>
            <caption className="sr-only">{label}</caption>
            <thead>
              <tr>
                <th scope="col">Period starting</th>
                <th scope="col">Value</th>
                <th scope="col" className="num">Samples collected</th>
              </tr>
            </thead>
            <tbody>
              {buckets.map((b) => (
                <tr key={b.start}>
                  <td>{new Date(b.start).toLocaleString()}</td>
                  <td>{describe(b)}</td>
                  <td className="num">{Math.round(b.coverage * 100)}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export function ChartLegend() {
  return (
    <div className="legend">
      <span><i className="swatch online" /> Measured</span>
      <span><i className="swatch offline" /> Server not running</span>
      <span><i className="swatch no_data" /> No data (Playkeeper not collecting)</span>
      <span><i className="swatch not_collected" /> Before collection started</span>
    </div>
  )
}
