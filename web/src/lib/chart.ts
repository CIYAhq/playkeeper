import type { BucketState, MetricsBucket } from '../api/types'

/** A bar of the players chart. */
export interface Bar {
  start: string
  /** Most players online in the period, when the server was running. */
  players: number | null
  state: BucketState
  coverage: number
}

/**
 * Merges buckets into bars `factor` buckets wide, lined up with the clock
 * (a 1-hour bar starts on the hour). A bar is "online" if the server ran at
 * any point in it, and then shows the most players seen; otherwise it takes
 * the state most of its buckets had.
 */
export function regroup(buckets: MetricsBucket[], factor: number, bucketSeconds = 0): Bar[] {
  const groups: MetricsBucket[][] = []
  const width = bucketSeconds * factor * 1000
  let key: number | undefined
  buckets.forEach((b, i) => {
    const k = width ? Math.floor(Date.parse(b.start) / width) : Math.floor(i / factor)
    if (k !== key) groups.push([])
    key = k
    groups[groups.length - 1]?.push(b)
  })
  const out: Bar[] = []
  for (const group of groups) {
    const first = group[0]
    if (!first) continue
    const online = group.filter((b) => b.state === 'online')
    const coverage = group.reduce((n, b) => n + b.coverage, 0) / group.length
    if (online.length) {
      out.push({ start: first.start, players: Math.max(...online.map((b) => b.playersMax ?? 0)), state: 'online', coverage })
      continue
    }
    const counts = new Map<BucketState, number>()
    for (const b of group) counts.set(b.state, (counts.get(b.state) ?? 0) + 1)
    const state = [...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? first.state
    out.push({ start: first.start, players: null, state, coverage })
  }
  return out
}

export function niceMax(v: number): number {
  if (v <= 1) return 1
  if (v <= 5) return Math.ceil(v)
  const pow = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (m * pow >= v) return m * pow
  }
  return 10 * pow
}

/** Chart y-axis ticks: 0, half and the top, as whole players. */
export function ticks(max: number): number[] {
  const top = Math.max(2, max % 2 === 0 ? max : max + 1)
  return [0, top / 2, top]
}
