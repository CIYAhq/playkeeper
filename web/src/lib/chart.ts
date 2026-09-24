import type { BucketState, MetricsBucket } from '../api/types'

export interface Point {
  index: number
  value: number
}

export interface Band {
  from: number
  to: number // exclusive bucket index
  state: Exclude<BucketState, 'online'>
}

/**
 * Splits buckets into continuous line segments of measured values. A bucket
 * without a value (server offline, collector down, before install) ends the
 * current segment: gaps are drawn as gaps, never as zeros.
 */
export function segments(buckets: MetricsBucket[], value: (b: MetricsBucket) => number | null): Point[][] {
  const out: Point[][] = []
  let cur: Point[] = []
  buckets.forEach((b, index) => {
    const v = b.state === 'online' ? value(b) : null
    if (v === null || v === undefined || !Number.isFinite(v)) {
      if (cur.length) out.push(cur)
      cur = []
      return
    }
    cur.push({ index, value: v })
  })
  if (cur.length) out.push(cur)
  return out
}

/** Merges consecutive non-online buckets of the same state into bands. */
export function bands(buckets: MetricsBucket[]): Band[] {
  const out: Band[] = []
  buckets.forEach((b, i) => {
    if (b.state === 'online') return
    const last = out[out.length - 1]
    if (last && last.state === b.state && last.to === i) last.to = i + 1
    else out.push({ from: i, to: i + 1, state: b.state })
  })
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

export function coverageSummary(buckets: MetricsBucket[]): { collected: number; noData: number; offline: number } {
  let collected = 0
  let noData = 0
  let offline = 0
  for (const b of buckets) {
    if (b.state === 'no_data') noData++
    else if (b.state === 'offline') offline++
    else if (b.state === 'online') collected++
  }
  return { collected, noData, offline }
}
