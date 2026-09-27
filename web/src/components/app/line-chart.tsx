import type { TimeLabel } from '@/lib/running'
import { cn } from '@/lib/utils'

// The design gives the chart colours as literal values, not tokens.
const green = '#15803D'
const amber = '#D97706'

const width = 1000
const height = 100

export interface LineRun {
  /** Index of the run's first value. */
  from: number
  values: number[]
  bad: boolean
}

/**
 * Splits a series into the stretches drawn in one colour. A gap (null) ends
 * a stretch; a segment touching a bad value is amber. Stretches that meet
 * share the point where the colour changes, so the line stays joined.
 */
export function lineRuns(values: (number | null)[], bad?: (v: number) => boolean): LineRun[] {
  const runs: LineRun[] = []
  let run: LineRun | undefined
  values.forEach((v, i) => {
    if (v === null) {
      run = undefined
      return
    }
    const prev = values[i - 1]
    if (prev === null || prev === undefined || !run) {
      run = { from: i, values: [v], bad: !!bad?.(v) }
      runs.push(run)
      return
    }
    const segmentBad = !!bad?.(prev) || !!bad?.(v)
    if (run.values.length === 1) run.bad = segmentBad
    if (segmentBad === run.bad) {
      run.values.push(v)
      return
    }
    run = { from: i - 1, values: [prev, v], bad: segmentBad }
    runs.push(run)
  })
  return runs
}

function paths(run: LineRun, count: number, max: number): { line: string; area?: string } {
  const x = (i: number) => (count <= 1 ? width / 2 : (i / (count - 1)) * width)
  const y = (v: number) => height - (Math.min(Math.max(v, 0), max) / max) * height
  const pts = run.values.map((v, k) => [x(run.from + k), y(v)] as const)
  const first = pts[0]
  const last = pts[pts.length - 1]
  if (!first || !last) return { line: '' }
  if (pts.length === 1) return { line: `M${first[0] - 3} ${first[1]}H${first[0] + 3}` }
  const line = pts.map(([px, py], k) => `${k ? 'L' : 'M'}${px.toFixed(1)} ${py.toFixed(2)}`).join('')
  return { line, area: `M${first[0].toFixed(1)} ${height}${line.replace(/^M/, 'L')}L${last[0].toFixed(1)} ${height}Z` }
}

/**
 * A line over time on a fixed scale from zero: green, amber where it is bad,
 * with an optional dashed line (a target or a limit) and its label.
 */
export function LineChart({
  values,
  max,
  bad,
  threshold,
  thresholdLabel,
  yLabels,
  xLabels,
  label,
  className,
  plotClassName = 'h-[88px]',
}: {
  values: (number | null)[]
  max: number
  bad?: (v: number) => boolean
  threshold?: number
  thresholdLabel?: string
  /** Top, middle and zero; none on a phone. */
  yLabels?: [string, string, string]
  xLabels: TimeLabel[]
  label: string
  className?: string
  plotClassName?: string
}) {
  const top = max > 0 ? max : 1
  const runs = lineRuns(values, bad).map((r) => ({ ...r, ...paths(r, values.length, top) }))
  const line = threshold !== undefined && threshold > 0 ? Math.min(threshold / top, 1) : undefined
  return (
    <div className={cn('flex gap-2', className)} role="img" aria-label={label}>
      {yLabels && (
        <div className={cn('relative w-8 shrink-0 text-right text-[11px] leading-none text-muted-foreground tabular-nums', plotClassName)} aria-hidden="true">
          {yLabels.map((text, i) => (
            <span key={i} className={cn('absolute right-0 -translate-y-1/2 whitespace-nowrap', i === 0 ? 'top-0' : i === 1 ? 'top-1/2' : 'top-full')}>
              {text}
            </span>
          ))}
        </div>
      )}
      <div className="min-w-0 flex-1">
        <div className={cn('relative', plotClassName)}>
          <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="absolute inset-0 size-full overflow-visible" aria-hidden="true">
            {runs.map((r) => r.area && <path key={`a${r.from}`} d={r.area} fill={r.bad ? amber : green} fillOpacity={0.07} />)}
            {runs.map((r) => (
              <path key={`l${r.from}`} d={r.line} fill="none" stroke={r.bad ? amber : green} strokeWidth={1.75} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
            ))}
          </svg>
          {line !== undefined && (
            <div className="pointer-events-none absolute inset-x-0 border-t border-dashed border-muted-foreground/45" style={{ top: `${(1 - line) * 100}%` }} aria-hidden="true">
              {thresholdLabel && <span className={cn('absolute right-0 rounded-sm bg-card/85 px-1 text-[11px] leading-4 whitespace-nowrap text-muted-foreground', line > 0.85 ? 'top-0.5' : 'bottom-0.5')}>{thresholdLabel}</span>}
            </div>
          )}
        </div>
        <div className="relative mt-1.5 h-4 text-[11px] text-muted-foreground" aria-hidden="true">
          {xLabels.map((l) => (
            <span key={l.at} className={cn('absolute whitespace-nowrap', l.at === 0 ? 'left-0' : l.at === 1 ? 'right-0' : '-translate-x-1/2')} style={l.at === 0 || l.at === 1 ? undefined : { left: `${l.at * 100}%` }}>
              {l.text}
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}
