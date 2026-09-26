import type { MemoryAdvice, MemoryFit, MemoryOption, ServerMemory } from '@/api/types'
import { t, type MessageKey } from '@/i18n'
import { formatMB } from '@/lib/format'
import { num, str } from '@/lib/params'
import { playersFor } from '@/lib/styles'

export interface Segment {
  key: string
  label: string
  memoryMB: number
  kind: 'system' | 'running' | 'stopped' | 'new' | 'free'
}

/**
 * The memory bar of the new-server memory step: the system's reserve, each
 * other server's share (stopped servers keep theirs), the new server, and
 * what is left. Shares never add up to more than the machine has.
 */
export function memorySegments(totalMB: number, systemMB: number, servers: ServerMemory[], newMB: number): Segment[] {
  const out: Segment[] = [{ key: 'system', label: '', memoryMB: Math.min(systemMB, totalMB), kind: 'system' }]
  let used = out[0]?.memoryMB ?? 0
  for (const s of servers) {
    const mb = Math.max(0, Math.min(s.memoryMB, totalMB - used))
    out.push({ key: s.id, label: s.name, memoryMB: mb, kind: s.running ? 'running' : 'stopped' })
    used += mb
  }
  const mine = Math.max(0, Math.min(newMB, totalMB - used))
  out.push({ key: 'new', label: '', memoryMB: mine, kind: 'new' })
  used += mine
  out.push({ key: 'free', label: '', memoryMB: Math.max(0, totalMB - used), kind: 'free' })
  return out
}

/** The share of the bar a segment takes, in percent. */
export function share(seg: Segment, totalMB: number): number {
  return totalMB > 0 ? (seg.memoryMB / totalMB) * 100 : 0
}

/**
 * Where Settings › Memory is on its way to a suggestion: measured days
 * towards the first three, then days towards a full week.
 */
export function memoryProgress(a: MemoryAdvice): { day: number; of: number } | undefined {
  if (a.verdict !== 'not_enough_data') return undefined
  const days = num(a.params, 'days') ?? 0
  if (days <= 0) return undefined
  const minDays = num(a.params, 'min_days') ?? 3
  if (days < minDays) return { day: days, of: minDays }
  const week = num(a.params, 'min_span_days') ?? 7
  return { day: Math.min(num(a.params, 'span_days') ?? days, week), of: week }
}

/** The line under Settings › Memory's budget: what the last 14 days say about it. */
export function memoryAdviceLine(a: MemoryAdvice, machine: string, type?: string): string {
  const p = a.params
  const count = num(p, 'days') ?? 0
  const peakMB = num(p, 'peak_mb')
  const peak = peakMB === undefined ? undefined : formatMB(peakMB)
  const memory = formatMB(a.budgetMB)
  switch (a.verdict) {
    case 'keep': {
      const reason = str(p, 'reason')
      if (reason === 'ran_short_once') return t('settings.memoryShortOnce', { count, memory })
      if (reason === 'ran_short_with_less') return t('settings.memoryShortWithLess', { count, memory })
      if (peak === undefined) return a.explanation
      return t(reason === 'tight' ? 'settings.memoryJustEnough' : 'settings.memoryPlenty', { count, peak, memory })
    }
    case 'lower': {
      const to = num(p, 'to_mb')
      return peak !== undefined && to ? t('settings.memoryLower', { count, peak, to: formatMB(to) }) : a.explanation
    }
    case 'raise': {
      const to = num(p, 'to_mb')
      return to ? t('settings.memoryRaise', { count, to: formatMB(to) }) : t('settings.memoryRaiseNoRoom', { count, machine })
    }
    case 'not_enough_data': {
      const until = { memory, count: playersFor(a.budgetMB, type) }
      const minDays = num(p, 'min_days') ?? 3
      if (!count && a.fromNextStart) return t('settings.memoryFromRestart', until)
      if (count >= minDays) return t('settings.memoryAfterWeek', until)
      return t('settings.memoryAfterDays', { ...until, days: minDays })
    }
    default: {
      const never: never = a.verdict
      return never
    }
  }
}

const fitText: Record<MemoryFit, MessageKey> = {
  too_tight: 'settings.memoryTooTight',
  little_room: 'settings.memoryLittleRoom',
  room_to_grow: 'settings.memoryRoomToGrow',
  more_than_needed: 'settings.memoryMoreThanNeeded',
}

const recommendedText: Record<MemoryFit, MessageKey> = {
  too_tight: 'settings.memoryRecommendedTight',
  little_room: 'settings.memoryRecommendedLittleRoom',
  room_to_grow: 'settings.memoryRecommendedRoom',
  more_than_needed: 'settings.memoryRecommendedMore',
}

/**
 * The line under a budget in Settings › Memory's list: whether the machine
 * has room for it, then how it would fit, or who it suits until then.
 */
export function memoryOptionHint(o: MemoryOption, advice: MemoryAdvice | undefined, machine: string, type?: string): string {
  if (!o.fits) return t('settings.memoryNoRoom', { machine })
  if (advice?.recommendedMB === o.memoryMB) return o.fit ? t(recommendedText[o.fit]) : t('settings.memoryRecommended')
  if (o.fit) return t(fitText[o.fit])
  return t('settings.memoryFriends', { count: playersFor(o.memoryMB, type) })
}

/**
 * The budgets Settings › Memory offers: the advice's, else the catalog's,
 * and always the server's own, smallest first.
 */
export function memoryOffers(currentMB: number, advice: MemoryAdvice | undefined, catalog: { memoryOptionsMB: number[]; maxMemoryMB: number } | undefined): MemoryOption[] {
  const out: MemoryOption[] = advice?.options.length
    ? [...advice.options]
    : catalog
      ? catalog.memoryOptionsMB.map((mb) => ({ memoryMB: mb, heapMB: 0, fits: mb <= catalog.maxMemoryMB || mb === currentMB }))
      : []
  if (currentMB > 0 && !out.some((o) => o.memoryMB === currentMB)) out.push({ memoryMB: currentMB, heapMB: 0, fits: true })
  return out.sort((a, b) => a.memoryMB - b.memoryMB)
}
