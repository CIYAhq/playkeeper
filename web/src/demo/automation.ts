// The live demo's automation: sleep, schedules and their runs, backup rules
// with the totals the agent works out while they're edited, copies on
// Backblaze B2, and the machine's disk space. Everything that would change a
// setting answers "not in the demo", as the engine does for writes it has no
// answer for; previews and estimates answer, since they change nothing.

import type {
  AutomaticBackups,
  BackupRulesView,
  DiskCandidate,
  DiskReport,
  DiskServer,
  DiskWay,
  OffsiteProvider,
  OffsiteView,
  RetentionEstimate,
  RetentionRules,
  RetentionSettings,
  RetentionText,
  Schedule,
  SchedulePreview,
  SchedulesResponse,
  ScheduleTiming,
  SleepView,
  Weekday,
} from '@/api/types'
import { cobblemonId, copiesSince, creativeId, defaultRules, demoUser, fakeSha, iso, serverOf, survivalEvery, survivalId, type DemoState, type Request, type Routes } from './data'
import { demoToast } from './toast'

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour
const mb = 1024 ** 2
const gb = 1024 ** 3

const madeAt = (s: DemoState) => s.hour * hour
const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone

// Sleep.

function sleep(s: DemoState, r: Request): SleepView {
  const srv = serverOf(s, r)
  const st = srv.sleep ?? { enabled: false, idleMinutes: 15, listening: false }
  const midnight = new Date(r.now)
  midnight.setHours(0, 0, 0, 0)
  const since = srv.phase === 'asleep' && st.asleepSince ? Math.max(Date.parse(st.asleepSince), midnight.getTime()) : undefined
  return { ...st, defaultIdleMinutes: 15, minIdleMinutes: 5, maxIdleMinutes: 24 * 60, today: since === undefined ? { count: 0, seconds: 0 } : { count: 1, seconds: Math.round((r.now - since) / 1000) } }
}

// Schedules.

const weekdays: Weekday[] = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

function clock(at: string | undefined): [number, number] {
  const [h = '0', m = '0'] = (at ?? '00:00').split(':')
  return [Number(h) || 0, Number(m) || 0]
}

/** When a timing runs next after `from`, up to `count` times, in this browser's time zone. Cron gets none: the demo doesn't read it. */
export function nextRuns(t: ScheduleTiming, from: number, count: number): string[] {
  const [h, m] = clock(t.at)
  const out: number[] = []
  const start = new Date(from)
  switch (t.kind) {
    case 'daily':
    case 'weekly':
      for (let d = 0; d < 400 && out.length < count; d++) {
        const at = new Date(start.getFullYear(), start.getMonth(), start.getDate() + d, h, m)
        if (at.getTime() > from && (t.kind === 'daily' || t.days?.includes(weekdays[at.getDay()] ?? 'sun'))) out.push(at.getTime())
      }
      break
    case 'interval': {
      const every = Math.max(1, t.everyHours ?? 24) * hour
      let at = new Date(start.getFullYear(), start.getMonth(), start.getDate(), h, m).getTime() - day
      while (at <= from) at += every
      for (; out.length < count; at += every) out.push(at)
      break
    }
    case 'once': {
      const [y = 0, mo = 1, d = 1] = (t.date ?? '').split('-').map(Number)
      const at = new Date(y, mo - 1, d, h, m).getTime()
      if (at > from) out.push(at)
      break
    }
    case 'cron':
      break
    default: {
      const unreachable: never = t.kind
      return unreachable
    }
  }
  return out.map(iso)
}

function summaryOf(t: ScheduleTiming): string {
  switch (t.kind) {
    case 'daily':
      return `Every day at ${t.at}`
    case 'weekly':
      return `Every ${(t.days ?? []).map((d) => d[0]?.toUpperCase() + d.slice(1)).join(', ')} at ${t.at}`
    case 'interval':
      return `Every ${t.everyHours} hours from ${t.at}`
    case 'once':
      return `Once, on ${t.date} at ${t.at}`
    case 'cron':
      return t.cron ?? ''
    default: {
      const unreachable: never = t.kind
      return unreachable
    }
  }
}

function schedule(s: DemoState, r: Request, serverId: string, id: string, made: number, sc: Pick<Schedule, 'kind' | 'payload'> & { timing: Omit<ScheduleTiming, 'timeZone'> }): Schedule {
  const timing = { ...sc.timing, timeZone: zone() }
  const last = s.runs[serverId]?.find((x) => x.scheduleId === id)
  return {
    id,
    serverId,
    kind: sc.kind,
    timing,
    payload: sc.payload,
    enabled: true,
    createdAt: iso(made),
    updatedAt: iso(made),
    createdBy: demoUser,
    updatedBy: demoUser,
    lastRun: last && { due: last.due, started: last.startedAt, finished: last.finishedAt, result: last.result, reason: last.reason, players: last.players },
    nextRun: nextRuns(timing, r.now, 1)[0],
    summary: summaryOf(timing),
  }
}

function schedules(s: DemoState, r: Request): SchedulesResponse {
  const srv = serverOf(s, r)
  const made = madeAt(s)
  switch (srv.id) {
    case survivalId:
      return {
        schedules: [
          schedule(s, r, srv.id, 'scsurvbk', made - 40 * day, { kind: 'backup', timing: { kind: 'interval', everyHours: survivalEvery, at: '00:00' }, payload: { onlyIfPlayed: true } }),
          schedule(s, r, srv.id, 'scsurvbn', made - 20 * day, { kind: 'announcement', timing: { kind: 'weekly', days: ['fri'], at: '19:30' }, payload: { message: 'Build night starts now: meet at spawn!' } }),
        ],
      }
    case creativeId:
      return { schedules: [schedule(s, r, srv.id, 'sccrebk', made - 18 * day, { kind: 'backup', timing: { kind: 'daily', at: '04:00' }, payload: { onlyIfPlayed: true } })] }
    case cobblemonId:
      return { schedules: [schedule(s, r, srv.id, 'sccobrs', made - 8 * day, { kind: 'restart', timing: { kind: 'daily', at: '05:00' }, payload: { warnSeconds: [300, 60], ifEmpty: 'now' } })] }
    default:
      return { schedules: [] }
  }
}

function preview(_s: DemoState, r: Request): SchedulePreview {
  const timing = (r.body as { timing?: ScheduleTiming } | undefined)?.timing
  if (!timing) return { valid: false, nextRuns: [] }
  if (timing.kind === 'weekly' && !timing.days?.length) return { valid: false, nextRuns: [], error: { error: 'Choose at least one day.', code: 'bad_timing', field: 'timing.days' } }
  const runs = nextRuns(timing, r.now, 3)
  if (timing.kind === 'once' && runs.length === 0) return { valid: false, nextRuns: [], error: { error: 'That time has passed.', code: 'bad_timing', field: 'timing.date' } }
  return { valid: true, nextRuns: runs, summary: summaryOf(timing) }
}

// Backup rules: what internal/backup/retention says, in the same words.

type Where = 'on-host' | 'off-site'
const place = (w: Where) => (w === 'off-site' ? 'off the server' : 'on this machine')
const plural = (n: number, one: string, many: string) => (n === 1 ? one : many)
const hoursSpan = (n: number) => (n === 1 ? 'hour' : n % 24 === 0 && n > 24 ? `${n / 24} days` : `${n} hours`)
const hoursPhrase = (n: number) => `every backup from the last ${hoursSpan(n)}`
const lastPhrase = (n: number) => (n === 1 ? 'the newest' : `the newest ${n}`)
const dailyPhrase = (n: number) => `one a day for ${n} ${plural(n, 'day', 'days')}`
const periodPhrase = (n: number, per: string, year: number, one: string, many: string) =>
  n === year ? `one a ${per} for a year` : n % year === 0 ? `one a ${per} for ${n / year} years` : `one a ${per} for ${n} ${plural(n, one, many)}`
const weeklyPhrase = (n: number) => periodPhrase(n, 'week', 52, 'week', 'weeks')
const monthlyPhrase = (n: number) => periodPhrase(n, 'month', 12, 'month', 'months')

function joinAnd(parts: string[]): string {
  return parts.length < 2 ? (parts[0] ?? '') : `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}`
}

function phrase(r: RetentionRules): string {
  if (r.keepAll) return 'every backup'
  return joinAnd([r.hours && hoursPhrase(r.hours), r.last && lastPhrase(r.last), r.daily && dailyPhrase(r.daily), r.weekly && weeklyPhrase(r.weekly), r.monthly && monthlyPhrase(r.monthly)].filter((x): x is string => !!x))
}

function params(r: RetentionRules, where: Where): Record<string, string> {
  return { where, keepAll: String(!!r.keepAll), hours: String(r.hours ?? 0), last: String(r.last ?? 0), daily: String(r.daily ?? 0), weekly: String(r.weekly ?? 0), monthly: String(r.monthly ?? 0) }
}

function describe(set: RetentionSettings): RetentionText[] {
  const always = ['pinned backups', 'the newest backup', 'the newest backup that passed its check', 'backups a restore or update may still need']
  if (!set.includeManual) always.push('backups made by hand')
  if (!set.deleteOnlyCopies) always.push('backups that are the only copy of an earlier world (from before a restore, or the last one on an earlier Minecraft version or of another world) until they are downloaded or copied off the server')
  const rules = (where: Where, r: RetentionRules): RetentionText => ({ code: 'rules', params: params(r, where), text: `Keeps ${phrase(r) || 'only the backups that are always kept'} ${place(where)}.` })
  return [
    rules('on-host', set.onHost),
    rules('off-site', set.offSite),
    { code: 'gaps', text: 'The last hours are counted back from the newest backup, and days, weeks and months without a backup don’t count, so a pause in backups never makes the rules delete older ones.' },
    { code: 'always_kept', params: { includeManual: String(!!set.includeManual), deleteOnlyCopies: String(!!set.deleteOnlyCopies) }, text: `Always kept: ${joinAnd(always)}.` },
  ]
}

/** "312 MB", "4.6 GB": decimal units, as the agent writes them. */
function humanBytes(n: number): string {
  if (n >= 1e12) return `${(n / 1e12).toFixed(1)} TB`
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`
  if (n >= 1e6) return `${Math.round(n / 1e6)} MB`
  if (n >= 1e3) return `${Math.round(n / 1e3)} kB`
  return `${n} B`
}

const dayStart = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
const periods: [keyof RetentionRules, (d: Date) => Date][] = [
  ['daily', dayStart],
  ['weekly', (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate() - ((d.getDay() + 6) % 7))],
  ['monthly', (d) => new Date(d.getFullYear(), d.getMonth(), 1)],
]

/** How many backups r keeps once automatic backups have run every `every` for long enough, the newest at `newest`: retention's steady. */
function steady(r: RetentionRules, newest: number, every: number, hours: number): number {
  const run = Math.max(hours, r.last ?? 0, 1)
  const more = new Set<number>()
  for (const [rule, first] of periods) {
    let k = 0
    for (let i = 0; i < Number(r[rule] ?? 0); i++) {
      if (k >= run) more.add(k)
      const begins = first(new Date(newest - k * every)).getTime()
      k = Math.max(Math.floor((newest - begins) / every) + 1, k + 1)
    }
  }
  return run + more.size
}

/** One place's rules in the editor, as retention's Estimate words them. */
export function estimate(set: RetentionSettings, where: Where, everyHours: number, bytes: number): RetentionEstimate {
  const r = where === 'off-site' ? set.offSite : set.onHost
  const manual = { rule: 'manual' as const, n: 0, count: 0, upTo: false, text: { code: 'estimate_manual', params: { includeManual: String(!!set.includeManual) }, text: set.includeManual ? 'Backups you make by hand: the rules above count them like the others.' : 'Backups you make by hand: kept until you delete them.' } }
  if (r.keepAll) {
    return { where, rows: [{ rule: 'keep_all', n: 0, count: -1, upTo: false, text: { code: 'estimate_keep_all', text: 'Every backup: kept until you delete it.' } }], count: -1, bytes: -1, summary: { code: 'estimate_keep_all', params: { where }, text: `Keeps every backup ${place(where)}, so they take more space over time.` } }
  }
  const every = everyHours * hour
  const hours = r.hours && every > 0 ? Math.ceil((r.hours * hour) / every) : 0
  const rows: RetentionEstimate['rows'] = []
  const add = (rule: 'hours' | 'last' | 'daily' | 'weekly' | 'monthly', n: number, count: number, upTo: boolean, label: string) => {
    const on = every > 0
    const text = label[0]?.toUpperCase() + label.slice(1) + (on ? (upTo ? `: up to ${count}` : `: ${count}`) : '')
    rows.push({ rule, n, count: on ? count : 0, upTo: on && upTo, text: { code: `estimate_${rule}`, params: { n: String(n), count: String(on ? count : 0), upTo: String(on && upTo) }, text: `${text}.` } })
  }
  if (r.hours) add('hours', r.hours, hours, true, hoursPhrase(r.hours))
  if (r.last) add('last', r.last, r.last, false, lastPhrase(r.last))
  if (r.daily) add('daily', r.daily, r.daily, false, dailyPhrase(r.daily))
  if (r.weekly) add('weekly', r.weekly, r.weekly, false, weeklyPhrase(r.weekly))
  if (r.monthly) add('monthly', r.monthly, r.monthly, false, monthlyPhrase(r.monthly))
  rows.push(manual)
  if (every <= 0) return { where, rows, count: 0, bytes: 0, summary: { code: 'estimate_off', params: { where }, text: 'Automatic backups are off, so the rules have nothing to keep yet.' } }
  // The overlap between the rules depends on when the newest backup is made, so try each hour of a week and take the most.
  const start = new Date(2026, 0, 5).getTime()
  let count = 0
  for (let h = 0; h < 7 * 24; h++) count = Math.max(count, steady(r, start + h * hour, every, hours))
  const sum = rows.reduce((n, x) => n + x.count, 0)
  const total = count * bytes
  const text = `About ${count} ${plural(count, 'backup', 'backups')}, roughly ${humanBytes(total)}, ${place(where)}.${sum > count ? ' Some backups count for more than one rule.' : ''}`
  return { where, rows, count, bytes: total, summary: { code: 'estimate', params: { where, count: String(count), bytes: String(total), rulesSum: String(sum) }, text } }
}

interface Rules {
  automatic: AutomaticBackups
  rules: RetentionSettings
  custom: boolean
}

/** Survival backs up every 6 hours by the rules Playkeeper starts with; Creative nightly, keeping more of its small world; Cobblemon's automatic backups aren't on yet. */
function rulesOf(s: DemoState, r: Request, serverId: string): Rules {
  const sc = schedules(s, { ...r, params: { id: serverId } }).schedules.find((x) => x.kind === 'backup')
  const auto = (everyHours: number): AutomaticBackups => ({ enabled: !!sc, everyHours, onlyIfPlayed: true, scheduleId: sc?.id, nextRun: sc?.nextRun })
  switch (serverId) {
    case survivalId:
      return { automatic: auto(survivalEvery), rules: defaultRules, custom: false }
    case creativeId:
      return { automatic: auto(24), rules: { onHost: { last: 10, daily: 14, weekly: 8 }, offSite: defaultRules.offSite }, custom: true }
    default:
      return { automatic: { enabled: false, everyHours: 24, onlyIfPlayed: true }, rules: defaultRules, custom: false }
  }
}

/** A typical backup's size, for the totals: the newest one's. */
const backupBytes = (s: DemoState, serverId: string) => s.backups[serverId]?.find((b) => b.kind !== 'rollback')?.sizeBytes ?? 0.5 * gb

function backupRules(s: DemoState, r: Request): BackupRulesView {
  const srv = serverOf(s, r)
  const { automatic, rules, custom } = rulesOf(s, r, srv.id)
  const every = automatic.enabled ? automatic.everyHours : 0
  const bytes = backupBytes(s, srv.id)
  return {
    automatic,
    rules,
    custom,
    describe: describe(rules),
    onHost: estimate(rules, 'on-host', every, bytes),
    offSite: estimate(rules, 'off-site', every, bytes),
    limits: { hours: 720, last: 1000, daily: 3660, weekly: 520, monthly: 240 },
  }
}

function backupEstimate(s: DemoState, r: Request): Record<'onHost' | 'offSite', RetentionEstimate> {
  const srv = serverOf(s, r)
  const { automatic, rules } = rulesOf(s, r, srv.id)
  const draft = (r.body as { rules?: RetentionSettings } | undefined)?.rules ?? rules
  const every = automatic.enabled ? automatic.everyHours : 0
  const bytes = backupBytes(s, srv.id)
  return { onHost: estimate(draft, 'on-host', every, bytes), offSite: estimate(draft, 'off-site', every, bytes) }
}

// Copies somewhere else: Survival's go to Backblaze B2; the others aren't set up yet.

const providers: OffsiteProvider[] = [
  { id: 'aws', name: 'Amazon S3', endpoint: 'https://s3.{region}.amazonaws.com', region: '', pathStyle: false, hint: 'Create an access key for an IAM user that may read, write, list and delete objects in the bucket.' },
  { id: 'b2', name: 'Backblaze B2', endpoint: 'https://s3.{region}.backblazeb2.com', region: '', pathStyle: false, hint: 'Create an application key for the bucket. The bucket page shows the endpoint; its region is the part after "s3.", for example us-west-004.' },
  { id: 'r2', name: 'Cloudflare R2', endpoint: 'https://{account}.r2.cloudflarestorage.com', region: 'auto', pathStyle: true, hint: 'Create an R2 API token with Object Read & Write for the bucket. The endpoint contains your account ID.' },
  { id: 'wasabi', name: 'Wasabi', endpoint: 'https://s3.{region}.wasabisys.com', region: '', pathStyle: false, hint: 'Create an access key for a user whose policy allows the bucket.' },
  { id: 'hetzner', name: 'Hetzner Object Storage', endpoint: 'https://{region}.your-objectstorage.com', region: 'fsn1', pathStyle: false, hint: 'Create S3 credentials in the Hetzner Cloud Console. The region is the bucket’s location, for example fsn1, nbg1 or hel1.' },
  { id: 'minio', name: 'MinIO', endpoint: '', region: 'us-east-1', pathStyle: true, hint: 'Use the address of your MinIO server with https:// and an access key allowed to use the bucket.' },
  { id: 'other', name: 'Other S3-compatible storage', endpoint: '', region: 'us-east-1', pathStyle: true, hint: 'Use the endpoint, region and access key your provider gives for its S3-compatible API.' },
]

/** A copy is on its way until its copiedAt; the view shows it uploading. */
const copied = (s: DemoState, serverId: string, now: number) => (s.copies[serverId] ?? []).filter((c) => Date.parse(c.copiedAt) <= now)

function offsite(s: DemoState, r: Request): OffsiteView {
  const srv = serverOf(s, r)
  if (srv.id !== survivalId) return { enabled: false, configured: false, type: '', place: '', copies: 0, copiesBytes: 0, queued: 0, providers }
  const list = copied(s, srv.id, r.now)
  const since = copiesSince(madeAt(s))
  const next = (s.copies[srv.id] ?? []).find((c) => Date.parse(c.copiedAt) > r.now)
  const lastCopy = [...list].sort((a, b) => b.copiedAt.localeCompare(a.copiedAt))[0]
  return {
    enabled: true,
    configured: true,
    type: 's3',
    place: 'Backblaze B2',
    s3: { provider: 'b2', endpoint: 'https://s3.eu-central-003.backblazeb2.com', region: 'eu-central-003', bucket: 'siya-minecraft-backups', prefix: 'survival/', accessKeyId: '003f4a9c2e7b1d50000000002', pathStyle: false, secretKeySet: true },
    key: { recipient: 'age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p', createdAt: iso(since), oldKeys: 0, savedAt: iso(since + 4 * minute), fileName: 'playkeeper-recovery-key-survival.txt' },
    lastCopy,
    copies: list.length,
    copiesBytes: list.reduce((n, c) => n + c.copySizeBytes, 0),
    pending: next && uploading(next, r.now),
    queued: 0,
    providers,
  }
}

function uploading(c: { backupId: string; fileName: string; createdAt: string; copiedAt: string; copySizeBytes: number }, now: number): OffsiteView['pending'] {
  const start = Date.parse(c.createdAt)
  const share = Math.min(1, Math.max(0, (now - start) / Math.max(1, Date.parse(c.copiedAt) - start)))
  return { backupId: c.backupId, fileName: c.fileName, uploading: true, sent: Math.round(c.copySizeBytes * share), total: c.copySizeBytes, bytesPerSec: 14 * mb, attempts: 1, backupCreatedAt: c.createdAt }
}

function recoveryKey() {
  demoToast('recoveryKey')
  return {}
}

// Disk space: the machine's 80 GB, what's on them, and what could go.

/** "Monday 2 January 2006", as internal/diskusage writes a day. */
function longDay(at: number): string {
  const d = new Date(at)
  const part = (o: Intl.DateTimeFormatOptions) => d.toLocaleDateString('en-GB', o)
  return `${part({ weekday: 'long' })} ${d.getDate()} ${part({ month: 'long' })} ${d.getFullYear()}`
}

function disk(s: DemoState, r: Request): DiskReport {
  const made = madeAt(s)
  const live = s.machine.live
  const total = live?.diskTotalBytes ?? 80 * gb
  const free = live?.diskFreeBytes ?? 41 * gb
  const candidates: DiskCandidate[] = []
  const survival = s.servers.find((x) => x.id === survivalId)
  if (survival) {
    for (let n = 0; n < 5; n++) {
      const at = made - (33 + n * 2) * day
      const date = new Date(at).toISOString().slice(0, 10)
      candidates.push({ id: `dlog${n}`, serverId: survivalId, kind: 'logs', reason: 'old_log', risk: 'low', path: `servers/survival/data/logs/${date}-1.log.gz`, bytes: Math.round((9 + n * 1.7) * mb), files: 1, modifiedAt: iso(at), params: { file: `${date}-1.log.gz`, date }, text: `A server log last written on ${longDay(at)}. Old logs only help to look into past problems.` })
    }
    candidates.push({ id: 'dpaper51', serverId: survivalId, kind: 'software', reason: 'unused_software', risk: 'low', path: 'servers/survival/data/versions/26.1.1', bytes: 52 * mb, files: 3, modifiedAt: iso(made - 4 * day), params: { path: 'versions/26.1.1', software: 'Paper', version: '26.1.1', build: '51' }, text: 'Paper 26.1.1 build 51, which this server no longer uses (versions/26.1.1). It is downloaded again if it’s ever needed.' })
  }
  const downloads: [string, number, number][] = [
    ['Cobblemon-fabric-1.7.1.jar', 84 * mb, 9],
    ['bluemap-5.13-paper.jar', 3.8 * mb, 17],
  ]
  for (const [i, [file, bytes, ago]] of downloads.entries()) {
    const at = made - ago * day
    candidates.push({ id: `ddl${i}`, kind: 'downloads', reason: 'downloaded', risk: 'low', path: `downloads/modrinth/${file}`, bytes: Math.round(bytes), files: 1, modifiedAt: iso(at), params: { file, date: new Date(at).toISOString().slice(0, 10) }, text: `Downloaded by Playkeeper on ${longDay(at)}. It is downloaded again if it’s ever needed.` })
  }
  const downloadIds = candidates.filter((c) => c.reason === 'downloaded').map((c) => c.id)
  const sum = (ids: string[]) => candidates.filter((c) => ids.includes(c.id)).reduce((n, c) => n + c.bytes, 0)
  const logIds = candidates.filter((c) => c.reason === 'old_log').map((c) => c.id)
  const ways: DiskWay[] = [
    ...(logIds.length ? [{ id: 'old_logs' as const, action: 'delete' as const, bytes: sum(logIds), candidateIds: logIds, serverIds: [survivalId], params: { days: '30' }, title: 'Old logs', text: '' }] : []),
    ...(survival ? [{ id: 'unused_software' as const, action: 'delete' as const, bytes: sum(['dpaper51']), candidateIds: ['dpaper51'], serverIds: [survivalId], versions: [{ software: 'Paper', version: '26.1.1', build: '51' }], title: 'Server software no longer used', text: '' }] : []),
    { id: 'downloads', action: 'delete', bytes: sum(downloadIds), candidateIds: downloadIds, everyServer: true, title: 'Downloads Playkeeper can fetch again', text: '' },
  ]
  const servers: DiskServer[] = s.servers.map((srv) => {
    const logs = candidates.filter((c) => c.serverId === srv.id && c.reason === 'old_log').reduce((n, c) => n + c.bytes, 0) + 24 * mb
    const backups = (s.backups[srv.id] ?? []).reduce((n, b) => n + b.sizeBytes, 0)
    const world = srv.worldBytes ?? 0.1 * gb
    const software = (srv.type === 'fabric' ? 310 : 230) * mb + (srv.id === survivalId ? 52 * mb : 0)
    const addons = (s.addons[srv.id]?.installed.length ?? 0) * (srv.type === 'fabric' ? 31 : 6) * mb
    const crash = srv.id === cobblemonId ? 3 * mb : 0
    const other = 6 * mb
    const kinds = [
      { kind: 'world', bytes: world, files: Math.round(world / (150 * 1024)) },
      { kind: 'backups', bytes: backups, files: (s.backups[srv.id]?.length ?? 0) * 2 },
      { kind: 'software', bytes: software, files: 140 },
      { kind: 'addons', bytes: addons, files: (s.addons[srv.id]?.installed.length ?? 0) * 12 },
      { kind: 'logs', bytes: logs, files: 14 },
      ...(crash ? [{ kind: 'crash_reports', bytes: crash, files: 2 }] : []),
      { kind: 'other', bytes: other, files: 30 },
    ]
    const bytes = kinds.reduce((n, k) => n + k.bytes, 0)
    return {
      id: srv.id,
      name: srv.name,
      total: { bytes, files: kinds.reduce((n, k) => n + k.files, 0) },
      kinds,
      groups: [
        { group: 'worlds', bytes: world },
        { group: 'backups', bytes: backups },
        { group: 'server_files', bytes: software + addons + other },
        { group: 'logs', bytes: logs + crash },
      ],
    }
  })
  const machine = [
    { kind: 'docker_image', bytes: 780 * mb, files: 1 },
    { kind: 'downloads', bytes: sum(downloadIds) + 64 * mb, files: 5 },
  ]
  const group = (g: 'worlds' | 'backups' | 'server_files' | 'logs') => servers.reduce((n, x) => n + (x.groups.find((y) => y.group === g)?.bytes ?? 0), 0)
  const files = group('server_files') + machine.reduce((n, k) => n + k.bytes, 0)
  const counted = group('worlds') + group('backups') + files + group('logs')
  return {
    scannedAt: iso(r.now - 2 * minute),
    disk: {
      dir: '/var/lib/playkeeper',
      total,
      free,
      used: total - free,
      bar: [
        { group: 'backups', bytes: group('backups') },
        { group: 'worlds', bytes: group('worlds') },
        { group: 'server_files', bytes: files },
        { group: 'logs', bytes: group('logs') },
        { group: 'other', bytes: Math.max(0, total - free - counted) },
        { group: 'free', bytes: free },
      ],
    },
    servers,
    machine,
    total: { bytes: counted, files: servers.reduce((n, x) => n + x.total.files, 0) + 4 },
    candidates,
    ways,
    freeable: ways.reduce((n, w) => n + w.bytes, 0),
    truncated: false,
  }
}

export const automationReads: Routes = {
  'GET /api/servers/:id/sleep': sleep,
  'GET /api/servers/:id/schedules': schedules,
  'GET /api/servers/:id/schedules/runs': (s, r) => ({ runs: (s.runs[serverOf(s, r).id] ?? []).slice(0, Number(r.query.get('limit') ?? 6) || 6) }),
  'POST /api/servers/:id/schedules/preview': preview,
  'GET /api/servers/:id/backup-rules': backupRules,
  'POST /api/servers/:id/backup-rules/estimate': backupEstimate,
  'GET /api/servers/:id/offsite': offsite,
  'GET /api/servers/:id/offsite/copies': (s, r) => ({ copies: copied(s, serverOf(s, r).id, r.now) }),
  'GET /api/servers/:id/offsite/recovery-key': recoveryKey,
  'GET /api/machines/:machine/disk': disk,
}

/** A new backup on a server whose copies are on: its copy arrives at Backblaze B2 a minute and a half later. */
export function copyNewBackup(s: DemoState, serverId: string, backup: { id: string; kind: string; createdAt: string; fileName: string; sizeBytes: number; minecraftVersion: string; levelName: string; sha256: string }, at: number) {
  const list = s.copies[serverId]
  if (serverId !== survivalId || !list) return
  list.unshift({ backupId: backup.id, kind: backup.kind, createdAt: backup.createdAt, fileName: backup.fileName, name: `${backup.fileName}.age`, sizeBytes: backup.sizeBytes, copySizeBytes: backup.sizeBytes + 4096, minecraftVersion: backup.minecraftVersion, levelName: backup.levelName, copiedAt: iso(at + 90_000), checked: iso(at + 90_000), onHost: true, sha256: backup.sha256 || fakeSha(at % 1_000_000) })
}
