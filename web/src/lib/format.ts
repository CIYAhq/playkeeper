import { formatLocale, t } from '@/i18n'

function num(v: number, digits = 0): string {
  return new Intl.NumberFormat(formatLocale(), { maximumFractionDigits: digits, minimumFractionDigits: 0 }).format(v)
}

/** The browser's IANA time zone, for routes that count local days. */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

/** A memory budget in MB, like "4 GB", "1.5 GB" or "768 MB". */
export function formatMB(mb: number): string {
  if (mb >= 1024) return t('unit.gb', { value: num(mb / 1024, 1) })
  return t('unit.mb', { value: num(mb) })
}

export function formatBytes(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  const keys = ['unit.bytes', 'unit.kb', 'unit.mb', 'unit.gb', 'unit.tb'] as const
  let v = n
  let i = 0
  while (v >= 1024 && i < keys.length - 1) {
    v /= 1024
    i++
  }
  return t(keys[i] ?? 'unit.tb', { value: num(v, v >= 10 || i === 0 ? 0 : 1) })
}

export function formatPercent(v: number | undefined | null): string {
  if (v === undefined || v === null || !Number.isFinite(v)) return '—'
  return t('unit.percent', { value: num(v) })
}

/** A length of time: "12 s", "3 m 20 s", "2 h 14 m" or "3 days". */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  const s = Math.floor(seconds)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return t('time.duration.days', { count: d })
  if (h > 0) return m > 0 ? t('time.duration.hm', { h, m }) : t('time.duration.hours', { count: h })
  if (m > 0) return m >= 10 ? t('time.duration.m', { m }) : t('time.duration.ms', { m, s: s % 60 })
  return t('time.duration.s', { s })
}

/** A calmer duration for things measured in days: "2 days", "5 hours", "12 minutes". */
export function formatSpan(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  if (s >= 86400) return t('time.duration.days', { count: Math.floor(s / 86400) })
  if (s >= 3600) return t('time.duration.hours', { count: Math.floor(s / 3600) })
  return t('time.duration.minutes', { count: Math.max(1, Math.floor(s / 60)) })
}

export function formatMs(ms: number): string {
  return ms < 1000 ? t('unit.ms', { value: num(ms) }) : t('unit.seconds', { value: num(ms / 1000, ms < 10_000 ? 1 : 0) })
}

export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return t('time.never')
  const diff = Math.round((now - new Date(iso).getTime()) / 1000)
  if (diff < 5) return t('time.justNow')
  if (diff < 60) return t('time.secondsAgo', { count: diff })
  if (diff < 3600) return t('time.minutesAgo', { count: Math.floor(diff / 60) })
  if (diff < 86400) return t('time.hoursAgo', { count: Math.floor(diff / 3600) })
  return t('time.daysAgo', { count: Math.floor(diff / 86400) })
}

/** A countdown: "0:48", "44:12" or "1:04:12". */
export function formatCountdown(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`
}

// toLocaleTimeString builds a new formatter on every call, which the console
// pays for every line; a formatter per locale formats the same text.
const clocks = new Map<string, Intl.DateTimeFormat>()

function clock(iso: string, seconds: boolean): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return String(d)
  const locale = formatLocale()
  const key = `${locale} ${seconds}`
  let f = clocks.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', second: seconds ? '2-digit' : undefined, hour12: false })
    clocks.set(key, f)
  }
  return f.format(d)
}

export function formatClock(iso: string): string {
  return clock(iso, false)
}

export function formatTime(iso: string): string {
  return clock(iso, true)
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(formatLocale(), { day: 'numeric', month: 'short' })
}

/** A date with its year, like "24 Dec 2026". */
export function formatLongDate(iso: string): string {
  return new Date(iso).toLocaleDateString(formatLocale(), { day: 'numeric', month: 'short', year: 'numeric' })
}

export function formatDateTime(iso: string | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString(formatLocale(), { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
}

export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

/** "Today 18:47", "Yesterday, 21:14" or "22 Sep 20:30". */
export function formatDay(iso: string, now: Date = new Date()): string {
  const d = new Date(iso)
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (sameDay(d, now)) return t('time.today', { time: formatClock(iso) })
  if (sameDay(d, yesterday)) return t('time.yesterday', { time: formatClock(iso) })
  return `${formatDate(iso)} ${formatClock(iso)}`
}

/** "a", "a and b", "a, b and c". */
export function formatList(items: string[]): string {
  if (items.length <= 1) return items[0] ?? ''
  if (items.length === 2) return t('common.list', { a: items[0] ?? '', b: items[1] ?? '' })
  return t('common.listMore', { items: items.slice(0, -1).join(', '), last: items[items.length - 1] ?? '' })
}

/** Join address shown to players: the host the admin used to open the panel. */
export function joinAddress(hostname: string, gamePort: number): string {
  const host = hostname.includes(':') && !hostname.startsWith('[') ? `[${hostname}]` : hostname
  return gamePort === 25565 ? host : `${host}:${gamePort}`
}

/** How long until a moment: "in 6 days", "in 5 h" or "in 12 min". */
export function timeUntil(iso: string, now: number = Date.now()): string {
  const s = Math.max(0, Math.floor((new Date(iso).getTime() - now) / 1000))
  if (s >= 86400) return t('time.inDays', { count: Math.floor(s / 86400) })
  if (s >= 3600) return t('time.inHours', { count: Math.floor(s / 3600) })
  return t('time.inMinutes', { count: Math.max(1, Math.floor(s / 60)) })
}

/** A server's join address: its friendly name once that works, else the panel's host with the port. */
export function serverJoinAddress(s: { joinAddress?: string; gamePort: number }, hostname: string = window.location.hostname): string {
  return s.joinAddress || joinAddress(hostname, s.gamePort)
}

/** How long ago, for things that change rarely: "just now", "5 days ago", "3 weeks ago", "4 months ago". */
export function relativeAge(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return t('time.never')
  const days = Math.floor((now - new Date(iso).getTime()) / 86_400_000)
  if (days < 14) return relativeTime(iso, now)
  if (days < 60) return t('time.weeksAgo', { count: Math.floor(days / 7) })
  if (days < 730) return t('time.monthsAgo', { count: Math.floor(days / 30) })
  return t('time.yearsAgo', { count: Math.floor(days / 365) })
}

/** A big count for lists: "3.1M", "640K". */
export function formatCompact(n: number): string {
  return new Intl.NumberFormat(formatLocale(), { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}
