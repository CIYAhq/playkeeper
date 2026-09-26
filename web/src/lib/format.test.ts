import { describe, expect, it } from 'vitest'
import { formatLocale } from '@/i18n'
import { formatClock, formatTime } from './format'

describe('clock times', () => {
  it('read the same as toLocaleTimeString', () => {
    for (const iso of ['2026-09-25T18:41:07.123456789Z', '2026-09-25T00:05:09Z', '2026-12-31T23:59:59.999Z']) {
      const d = new Date(iso)
      expect(formatTime(iso)).toBe(d.toLocaleTimeString(formatLocale(), { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }))
      expect(formatClock(iso)).toBe(d.toLocaleTimeString(formatLocale(), { hour: '2-digit', minute: '2-digit', hour12: false }))
    }
  })

  it('say a time that is not one is invalid instead of throwing', () => {
    expect(formatClock('not a time')).toBe('Invalid Date')
    expect(formatTime('')).toBe('Invalid Date')
  })
})
