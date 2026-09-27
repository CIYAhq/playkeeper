import { formatLocale, t } from '@/i18n'
import { formatClock, formatDate } from './format'

/** The viewer's IANA time zone, which schedules are saved in and times are shown in. */
export function viewerTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** The viewer's offset from UTC as the page names it: "UTC+3", "UTC−4:30" or "UTC". */
export function zoneLabel(now: Date = new Date()): string {
  const minutes = -now.getTimezoneOffset()
  if (minutes === 0) return t('when.utc')
  const sign = minutes > 0 ? '+' : '−'
  const h = Math.floor(Math.abs(minutes) / 60)
  const m = Math.abs(minutes) % 60
  return t('when.utcOffset', { offset: `${sign}${h}${m ? `:${String(m).padStart(2, '0')}` : ''}` })
}

function dayStart(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

/** Whole calendar days from now to iso in the viewer's time zone: 0 today, 1 tomorrow, −1 yesterday. */
export function daysFrom(iso: string, now: Date = new Date()): number {
  return Math.round((dayStart(new Date(iso)) - dayStart(now)) / 86_400_000)
}

export function weekdayName(iso: string, style: 'long' | 'short' = 'long'): string {
  return new Date(iso).toLocaleDateString(formatLocale(), { weekday: style })
}

/** A time to come: "today 05:00", "tomorrow 05:00", "Friday 20:00" or "3 Oct 20:00". */
export function upcoming(iso: string, now: Date = new Date()): string {
  const time = formatClock(iso)
  const days = daysFrom(iso, now)
  if (days <= 0) return t('when.today', { time })
  if (days === 1) return t('when.tomorrow', { time })
  if (days < 7) return t('when.weekday', { day: weekdayName(iso), time })
  return t('when.date', { date: formatDate(iso), time })
}

/** upcoming() for the middle of a sentence: "tomorrow at 05:00". */
export function upcomingAt(iso: string, now: Date = new Date()): string {
  const time = formatClock(iso)
  const days = daysFrom(iso, now)
  if (days <= 0) return t('when.todayAt', { time })
  if (days === 1) return t('when.tomorrowAt', { time })
  if (days < 7) return t('when.weekdayAt', { day: weekdayName(iso), time })
  return t('when.dateAt', { date: formatDate(iso), time })
}

export const weekdays = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const

/** A weekday's name from its code: "mon" is "Monday". */
export function dayName(day: string, style: 'long' | 'short' = 'long'): string {
  const i = weekdays.indexOf(day as (typeof weekdays)[number])
  // 5 January 2026 is a Monday.
  return new Date(2026, 0, 5 + Math.max(0, i)).toLocaleDateString(formatLocale(), { weekday: style })
}

/** A time gone by, as a day: "today 05:00", "yesterday 18:00", "last Friday" or "22 Sep". */
export function past(iso: string, now: Date = new Date()): string {
  const days = daysFrom(iso, now)
  if (days >= 0) return t('when.today', { time: formatClock(iso) })
  if (days === -1) return t('when.yesterday', { time: formatClock(iso) })
  if (days > -7) return t('when.lastWeekday', { day: weekdayName(iso) })
  return formatDate(iso)
}

/** A row's day and time in a list of runs: "Today 05:00", "Yesterday 18:00", "Fri 20:00" or "22 Sep 20:00". */
export function runDay(iso: string, now: Date = new Date()): string {
  const time = formatClock(iso)
  const days = daysFrom(iso, now)
  if (days === 0) return t('when.runToday', { time })
  if (days === -1) return t('when.runYesterday', { time })
  if (days > -7 && days < 0) return t('when.weekday', { day: weekdayName(iso, 'short'), time })
  return t('when.date', { date: formatDate(iso), time })
}

/** Capitalises a phrase's first letter, for phrases that can start a sentence. */
export function sentence(text: string): string {
  return text.charAt(0).toLocaleUpperCase(formatLocale()) + text.slice(1)
}
