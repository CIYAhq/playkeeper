import { describe, expect, it } from 'vitest'
import { bottomSlack, followAfterScroll, isAtBottom, keepTail, mergeByTime } from './console'

describe('the console follows new lines only at the bottom', () => {
  const box = (scrollTop: number) => ({ scrollTop, scrollHeight: 5000, clientHeight: 400 })

  it('counts the end, and a few pixels short of it, as the bottom', () => {
    expect(isAtBottom(box(4600))).toBe(true)
    expect(isAtBottom(box(4600 - bottomSlack))).toBe(true)
    expect(isAtBottom(box(4600 - bottomSlack - 1))).toBe(false)
    expect(isAtBottom(box(0))).toBe(false)
    expect(isAtBottom({ scrollTop: 0, scrollHeight: 300, clientHeight: 400 })).toBe(true)
    // Phones overscroll past the end while bouncing.
    expect(isAtBottom(box(4650))).toBe(true)
  })

  it('stops following when the reader scrolls up and starts again at the bottom', () => {
    expect(followAfterScroll(true, false, false)).toBe(false)
    expect(followAfterScroll(false, false, false)).toBe(false)
    expect(followAfterScroll(false, true, false)).toBe(true)
    expect(followAfterScroll(true, true, false)).toBe(true)
  })

  it('keeps following while a jump to the latest line is on its way down', () => {
    expect(followAfterScroll(true, false, true)).toBe(true)
    expect(followAfterScroll(true, true, true)).toBe(true)
  })
})

describe('the console keeps a tail of the log', () => {
  const n = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => from + i)

  it('appends new lines and drops the oldest past the limit', () => {
    expect(keepTail(n(1, 3), n(4, 5), false, 10)).toEqual(n(1, 5))
    expect(keepTail(n(1, 1800), n(1801, 2500), false, 2000)).toEqual(n(501, 2500))
  })

  it('starts again when the log restarted', () => {
    expect(keepTail(n(1, 50), n(1, 3), true, 2000)).toEqual(n(1, 3))
    expect(keepTail(n(1, 50), n(1, 2600), true, 2000)).toEqual(n(601, 2600))
  })

  it('keeps the same list when nothing new came in', () => {
    const kept = n(1, 5)
    expect(keepTail(kept, [], false, 2000)).toBe(kept)
    expect(keepTail(kept, [], true, 2000)).toEqual([])
  })
})

describe('the console puts sent commands among the lines by time', () => {
  const line = (ts: string, id: string) => ({ ts, id })

  it('interleaves both lists and keeps each in its own order', () => {
    const lines = [line('2026-09-25T18:00:00.100456789Z', 'l1'), line('2026-09-25T18:00:02Z', 'l2'), line('2026-09-25T18:00:04Z', 'l3')]
    const sent = [line('2026-09-25T18:00:01.000Z', 's1'), line('2026-09-25T18:00:01.000Z', 'r1'), line('2026-09-25T18:00:09.000Z', 's2')]
    expect(mergeByTime(lines, sent).map((x) => x.id)).toEqual(['l1', 's1', 'r1', 'l2', 'l3', 's2'])
  })

  it('puts log lines first when the times are equal', () => {
    expect(mergeByTime([line('2026-09-25T18:00:02Z', 'l')], [line('2026-09-25T18:00:02.000Z', 's')]).map((x) => x.id)).toEqual(['l', 's'])
  })

  it('returns the lines themselves when nothing was sent', () => {
    const lines = [line('2026-09-25T18:00:00Z', 'l1')]
    expect(mergeByTime(lines, [])).toBe(lines)
    expect(mergeByTime([], lines)).toBe(lines)
  })
})
