import { describe, expect, it } from 'vitest'
import type { MetricsBucket, ServerStatus } from '../api/types'
import { bands, niceMax, segments } from './chart'
import { formatBytes, formatDuration, joinAddress } from './format'
import { controls, phaseTone, stepIndex } from './phase'

const b = (state: MetricsBucket['state'], playersMax: number | null = null): MetricsBucket => ({
  start: '2026-09-24T00:00:00Z',
  playersMax,
  cpuAvg: null,
  memAvg: null,
  coverage: state === 'no_data' ? 0 : 1,
  state,
})

describe('chart gaps', () => {
  it('breaks the line at offline and missing buckets instead of drawing zeros', () => {
    const buckets = [b('online', 2), b('online', 3), b('offline'), b('no_data'), b('online', 0), b('online', 1)]
    const segs = segments(buckets, (x) => x.playersMax)
    expect(segs).toEqual([
      [{ index: 0, value: 2 }, { index: 1, value: 3 }],
      [{ index: 4, value: 0 }, { index: 5, value: 1 }],
    ])
  })

  it('keeps a real zero as a value', () => {
    expect(segments([b('online', 0)], (x) => x.playersMax)).toEqual([[{ index: 0, value: 0 }]])
  })

  it('merges consecutive states into bands', () => {
    const buckets = [b('not_collected'), b('not_collected'), b('online', 1), b('no_data'), b('no_data'), b('offline')]
    expect(bands(buckets)).toEqual([
      { from: 0, to: 2, state: 'not_collected' },
      { from: 3, to: 5, state: 'no_data' },
      { from: 5, to: 6, state: 'offline' },
    ])
  })

  it('picks readable axis maxima', () => {
    expect(niceMax(0)).toBe(1)
    expect(niceMax(3)).toBe(3)
    expect(niceMax(7)).toBe(10)
    expect(niceMax(42)).toBe(50)
  })
})

describe('formatting', () => {
  it('formats sizes and durations', () => {
    expect(formatBytes(5205991)).toBe('5.0 MB')
    expect(formatBytes(undefined)).toBe('—')
    expect(formatDuration(3725)).toBe('1h 2m')
    expect(formatDuration(59)).toBe('59s')
  })

  it('builds the join address players type', () => {
    expect(joinAddress('203.0.113.10', 25565)).toBe('203.0.113.10')
    expect(joinAddress('203.0.113.10', 25566)).toBe('203.0.113.10:25566')
    expect(joinAddress('2001:db8::1', 25565)).toBe('[2001:db8::1]')
    expect(joinAddress('mc.example.org', 25565)).toBe('mc.example.org')
  })
})

describe('state-aware controls', () => {
  const base: ServerStatus = { exists: true, desired: 'running', phase: 'online', reachable: true, gamePort: 25565, offlineModeTest: false, crashCount: 0, pendingRestart: false, agentVersion: 'test' }
  it('offers stop/restart when online and start when stopped', () => {
    expect(controls(base)).toMatchObject({ canStart: false, canStop: true, canRestart: true })
    expect(controls({ ...base, phase: 'stopped' })).toMatchObject({ canStart: true, canStop: false, canRestart: false })
    expect(controls({ ...base, phase: 'crashed' })).toMatchObject({ canStart: true, canStop: false })
  })
  it('disables everything while an operation runs or Docker is down', () => {
    const op = { id: 'x', kind: 'backup', status: 'running' as const, phase: 'archiving', actor: 'admin', startedAt: '' }
    expect(controls({ ...base, operation: op })).toMatchObject({ canStart: false, canStop: false, canRestart: false, busy: true })
    expect(controls({ ...base, phase: 'docker_unavailable' })).toMatchObject({ canStart: false, canStop: false, canRestart: false })
  })
  it('maps phases to tones and steps', () => {
    expect(phaseTone('online')).toBe('good')
    expect(phaseTone('crashed')).toBe('bad')
    expect(stepIndex('verifying_download')).toBe(1)
    expect(stepIndex('online')).toBe(4)
  })
})
