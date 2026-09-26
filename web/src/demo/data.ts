// The live demo's sample data: a made-up VPS with two Paper servers, their
// players, console, backups and history. Every GET the dashboard makes is
// answered from `reads` at the bottom; a path that isn't there gets "no
// sample data" and its screen shows its empty state. To give a screen sample
// data, add what it needs to DemoState and sample(), and its path to reads.

import { ApiError } from '@/api/client'
import type {
  Activity,
  AgentActivity,
  ApiToken,
  AuditEntry,
  Backup,
  Catalog,
  CatalogEntry,
  DailyActivity,
  LogLine,
  LogsResponse,
  MachineLinkInfo,
  MachineView,
  Me,
  MetricsBucket,
  MetricsResponse,
  OperatorEntry,
  Phase,
  PlayerStat,
  PlayersSummary,
  Preflight,
  ServerConfig,
  ServerStatus,
  Session,
  SessionsResponse,
  UpdateInfo,
  WhitelistEntry,
} from '@/api/types'
import { t } from '@/i18n'
import { faceCount, faceIndex } from './faces'

/** Bump when DemoState changes shape, so sessions saved by an older demo start over. */
export const sampleVersion = 1
export const demoVersion = '0.4.0'
export const demoUser = 'siya'
export const machineId = 'q7m2vk9xpd'
/** Everyone on Survival's list. Each gets a face of their own, in this order. */
export const samplePlayers = ['JunoFox', 'tobi2009', 'mara_k', 'PixelPia', 'Brickbert', 'Kestrel_7']

/** A player's face: the sample players' own, and one picked by name for anyone added in the demo. */
export function faceOf(name: string): number {
  const i = samplePlayers.indexOf(name)
  return i >= 0 ? i % faceCount : faceIndex(name)
}
export const image = 'itzg/minecraft-server:2026.9.1-java25'

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour
const gb = 1024 ** 3

/** A server's life beyond its status: who's on since when, and what happens next. */
export interface Live {
  online: { name: string; since: number }[]
  /** Players who come back on their own, after a restart or a kick. */
  joins: { name: string; at: number }[]
  /** Players who left when the server stopped, and join again once it's back. */
  away: string[]
  /** When the next console line arrives, and which one. */
  nextLine: number
  cursor: number
}

/** One moment of a job: the phase it reaches and what the console says then. */
export interface Step {
  at: number
  phase?: Phase
  op?: string
  lines?: string[]
  /** Everyone online leaves, as they do when a server stops. */
  leave?: boolean
}

export type JobKind = 'start' | 'stop' | 'restart' | 'backup' | 'update-version' | 'restore' | 'create'

/** A job playing out on a server; its last step finishes it. */
export interface Job {
  kind: JobKind
  serverId: string
  start: number
  steps: Step[]
  reached: number
  /** What finishing changes: the version to update to, the backup's note. */
  args: Record<string, string>
}

export interface DemoState {
  sample: number
  /** The hour this state was made in; the demo starts over when it changes. */
  hour: number
  seq: number
  machine: MachineView
  servers: ServerStatus[]
  live: Record<string, Live>
  backups: Record<string, Backup[]>
  logs: Record<string, LogLine[]>
  whitelist: Record<string, WhitelistEntry[]>
  operators: Record<string, OperatorEntry[]>
  /** Players who've played on each server, with their totals before today. */
  roster: Record<string, PlayerStat[]>
  activity: Activity[]
  audit: AuditEntry[]
  tokens: ApiToken[]
  agentActivity: AgentActivity[]
  prefs: Record<string, string>
  jobs: Record<string, Job>
}

export const iso = (ms: number) => new Date(ms).toISOString()

/** A repeatable pseudo-random number in [0, 1) for a seed. */
export function noise(seed: number): number {
  let x = Math.imul(seed ^ 0x9e3779b9, 0x85ebca6b)
  x = Math.imul(x ^ (x >>> 13), 0xc2b2ae35)
  return ((x ^ (x >>> 16)) >>> 0) / 2 ** 32
}

/** 64 hex digits that look like a SHA-256, the same for the same seed. */
export function fakeSha(seed: number): string {
  let out = ''
  for (let i = 0; out.length < 64; i++) out += Math.floor(noise(seed * 131 + i) * 2 ** 32).toString(16).padStart(8, '0')
  return out.slice(0, 64)
}

/** "[21:04:12 INFO]: …", the way Paper writes its console. */
export function logText(at: number, text: string, level = 'INFO'): string {
  const d = new Date(at)
  const two = (n: number) => String(n).padStart(2, '0')
  return `[${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())} ${level}]: ${text}`
}

// What players say and do on the console, in turn; {a}, {b} and {c} are
// whoever is online.
export const chatter = [
  '<{a}> anyone up for the nether?',
  '<{b}> give me 5 min, sorting my chests',
  '{a} has made the advancement [We Need to Go Deeper]',
  '<{b}> omw',
  '{b} was blown up by Creeper',
  '<{b}> my house!!',
  '<{a}> lol',
  '{c} has made the advancement [Monster Hunter]',
  '<{c}> who took my diamond pickaxe',
  '<{a}> it was like that when I got here',
  '{b} fell from a high place',
  '<{b}> ok who put lava there',
  '{a} has completed the challenge [Return to Sender]',
  '<{c}> brb, dinner',
]

// The shape of an evening: most players online per local hour of the day.
const busy = [0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 1, 2, 2, 3, 4, 5, 6, 5, 4, 2, 1]

const survivalId = 'h4k8v2m9qa'
const creativeId = 'c6t3w8n2rb'

export function config(over: Partial<ServerConfig> & Pick<ServerConfig, 'minecraftVersion' | 'memoryMB' | 'motd' | 'createdAt'>): ServerConfig {
  return {
    type: 'paper',
    versionId: `paper-${over.minecraftVersion}`,
    paperBuild: 74,
    jarSha256: fakeSha(over.memoryMB),
    heapMB: Math.round(over.memoryMB * 0.75),
    levelName: 'world',
    jarVerifiedAt: over.createdAt,
    maxPlayers: 10,
    whitelist: true,
    eulaAcceptedAt: over.createdAt,
    eulaAcceptedBy: demoUser,
    image,
    ...over,
  }
}

function backup(serverId: string, n: number, at: number, sizeBytes: number, over: Partial<Backup> = {}): Backup {
  const stamp = iso(at).slice(0, 16).replace(/[-:T]/g, '')
  return {
    id: `bk${serverId.slice(0, 4)}${n}`,
    serverId,
    kind: 'manual',
    createdAt: iso(at),
    fileName: `${serverId === survivalId ? 'survival' : 'creative'}-${stamp}.tar.gz`,
    sizeBytes,
    sha256: fakeSha(at % 100_000),
    location: 'local',
    verified: true,
    verifiedAt: iso(at + 2 * minute),
    downtimeMs: 0,
    minecraftVersion: serverId === survivalId ? '26.1.2' : '26.2.1',
    levelName: 'world',
    fileCount: Math.round(sizeBytes / 150_000),
    createdBy: demoUser,
    ...over,
  }
}

function stat(name: string, lastSeen: number, sessions: number, hours: number, online = false): PlayerStat {
  return { name, lastSeen: iso(lastSeen), online, sessions, playtimeSeconds: Math.round(hours * 3600) }
}

/** The demo as it starts: a fresh copy every hour and on every new visit. */
export function sample(now: number): DemoState {
  const created = now - 41 * day
  const creativeCreated = now - 19 * day
  const stoppedAt = now - 2 * day - 3 * hour
  const survivalBackups = [
    backup(survivalId, 3, now - 62 * minute, 1.21 * gb, { note: 'Before the nether hub' }),
    backup(survivalId, 2, now - day - 5 * hour, 1.18 * gb),
    backup(survivalId, 1, now - 4 * day, 1.09 * gb, { kind: 'rollback', note: undefined, downtimeMs: 4200 }),
  ]
  const creativeBackups = [backup(creativeId, 1, stoppedAt - 20 * minute, 0.37 * gb)]
  const online = [
    { name: 'JunoFox', since: now - 12 * minute },
    { name: 'tobi2009', since: now - 48 * minute },
    { name: 'mara_k', since: now - 97 * minute },
  ]
  const survival: ServerStatus = {
    id: survivalId,
    name: 'Survival',
    slug: 'survival',
    game: 'minecraft-java',
    type: 'paper',
    createdAt: iso(created),
    machineId,
    exists: true,
    desired: 'running',
    phase: 'online',
    reachable: true,
    reachableAt: iso(now),
    startedAt: iso(now - 26 * hour),
    players: { online: online.length, max: 10, names: online.map((p) => p.name), source: 'rcon list', at: iso(now) },
    config: config({ minecraftVersion: '26.1.2', memoryMB: 4096, motd: 'Survival with friends', createdAt: iso(created), playStyle: 'friends' }),
    gameplay: { difficulty: 'normal', pvp: false, gameMode: 'survival', hardcore: false, viewDistance: 10, levelType: 'normal' },
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    resources: { cpuPercent: 14, tps: 20, memBytes: 2.4 * gb, memLimitBytes: 4 * gb, diskFreeBytes: 41 * gb, diskTotalBytes: 80 * gb, at: iso(now) },
    lastBackup: survivalBackups[0],
    worldBytes: 1.24 * gb,
    pendingRestart: false,
    collectingSince: iso(created),
    firstSteps: { invited: 'JunoFox', friendJoined: 'JunoFox', friendJoinedAt: iso(created + day), backedUp: true, downloaded: true },
  }
  const creative: ServerStatus = {
    id: creativeId,
    name: 'Creative',
    slug: 'creative',
    game: 'minecraft-java',
    type: 'paper',
    createdAt: iso(creativeCreated),
    machineId,
    exists: true,
    desired: 'stopped',
    phase: 'stopped',
    reachable: false,
    stoppedAt: iso(stoppedAt),
    config: config({ minecraftVersion: '26.2.1', paperBuild: 12, memoryMB: 3072, motd: 'Build anything', createdAt: iso(creativeCreated), playStyle: 'creative', maxPlayers: 8 }),
    gameplay: { difficulty: 'peaceful', pvp: false, gameMode: 'creative', hardcore: false, viewDistance: 12, levelType: 'flat' },
    gamePort: 25566,
    offlineModeTest: false,
    crashCount: 0,
    lastBackup: creativeBackups[0],
    worldBytes: 0.38 * gb,
    pendingRestart: false,
    collectingSince: iso(creativeCreated),
    firstSteps: { invited: 'PixelPia', friendJoined: 'PixelPia', friendJoinedAt: iso(creativeCreated + hour), backedUp: true, downloaded: true },
  }
  return {
    sample: sampleVersion,
    hour: Math.floor(now / hour),
    seq: 1,
    machine: {
      id: machineId,
      projectId: 'demo',
      name: 'my-vps',
      kind: 'local',
      live: {
        hostname: 'my-vps',
        os: 'A made-up machine',
        arch: 'amd64',
        cpus: 4,
        cpuPercent: 22,
        memoryTotalMB: 16384,
        systemReserveMB: 1536,
        serversMemoryMB: 7168,
        memoryFreeMB: 7680,
        diskFreeBytes: 41 * gb,
        diskTotalBytes: 80 * gb,
        docker: true,
        dockerVersion: '28.4.0',
        agentVersion: demoVersion,
        defaultGamePort: 25565,
        offlineModeTest: false,
        servers: 2,
      },
    },
    servers: [survival, creative],
    live: {
      [survivalId]: { online, joins: [], away: [], nextLine: now + 4000, cursor: 0 },
      [creativeId]: { online: [], joins: [], away: ['PixelPia'], nextLine: now, cursor: 0 },
    },
    backups: { [survivalId]: survivalBackups, [creativeId]: creativeBackups },
    logs: { [survivalId]: survivalLog(now, online), [creativeId]: creativeLog(stoppedAt) },
    whitelist: {
      [survivalId]: samplePlayers.map((name) => ({ name })),
      [creativeId]: ['PixelPia', 'Brickbert', 'JunoFox'].map((name) => ({ name })),
    },
    operators: { [survivalId]: [{ name: 'JunoFox', level: 4 }], [creativeId]: [{ name: 'PixelPia', level: 4 }] },
    roster: {
      [survivalId]: [
        stat('JunoFox', now, 58, 96.5, true),
        stat('tobi2009', now, 41, 62, true),
        stat('mara_k', now, 33, 51.2, true),
        stat('PixelPia', now - 5 * hour, 27, 38.4),
        stat('Brickbert', now - day - 3 * hour, 12, 14.1),
        stat('Kestrel_7', now - 6 * day, 4, 3.2),
      ],
      [creativeId]: [stat('PixelPia', stoppedAt - 2 * hour, 19, 31), stat('Brickbert', stoppedAt - day, 8, 9.5), stat('JunoFox', stoppedAt - 4 * day, 3, 2.1)],
    },
    activity: [
      { ts: iso(now - 12 * minute), serverId: survivalId, kind: 'joined', player: 'JunoFox' },
      { ts: iso(now - 48 * minute), serverId: survivalId, kind: 'joined', player: 'tobi2009' },
      { ts: iso(now - 62 * minute), serverId: survivalId, kind: 'backup', actor: demoUser },
      { ts: iso(stoppedAt), serverId: creativeId, kind: 'stopped' },
    ],
    audit: [
      { id: 6, ts: iso(now - 62 * minute), actor: demoUser, action: 'backup.created', target: 'Survival', serverId: survivalId, result: 'succeeded', source: 'agent', machineId },
      { id: 5, ts: iso(now - 70 * minute), actor: demoUser, action: 'login', target: 'panel', result: 'succeeded', source: 'panel' },
      { id: 4, ts: iso(stoppedAt), actor: demoUser, action: 'stop', target: 'Creative', serverId: creativeId, result: 'succeeded', source: 'agent', machineId },
      { id: 3, ts: iso(now - 4 * day), actor: demoUser, action: 'server.version', target: 'Survival', serverId: survivalId, result: 'succeeded', detail: '26.1.1 → 26.1.2', source: 'agent', machineId },
      { id: 2, ts: iso(creativeCreated), actor: demoUser, action: 'server.create', target: 'Creative', serverId: creativeId, result: 'succeeded', source: 'agent', machineId },
      { id: 1, ts: iso(created), actor: demoUser, action: 'setup', target: 'admin', result: 'succeeded', detail: 'first admin account created', source: 'panel' },
    ],
    tokens: [
      { id: 'tkdemo1', name: 'Claude on my laptop', role: 'moderator', allServers: true, servers: [], createdAt: iso(now - 6 * day), expiresAt: iso(now + 24 * day), lastUsedAt: iso(now - 2 * hour), account: demoUser, mine: true },
    ],
    agentActivity: [
      { tokenId: 'tkdemo1', tokenName: 'Claude on my laptop', tool: 'list_online_players', serverId: survivalId, serverName: 'Survival', count: 3, at: iso(now - 2 * hour) },
      { tokenId: 'tkdemo1', tokenName: 'Claude on my laptop', tool: 'send_chat_message', serverId: survivalId, serverName: 'Survival', count: 1, at: iso(now - 2 * hour - 5 * minute) },
    ],
    prefs: {},
    jobs: {},
  }
}

function survivalLog(now: number, online: { name: string; since: number }[]): LogLine[] {
  const lines: string[][] = []
  const at = (ms: number, text: string) => lines.push([iso(ms), logText(ms, text)])
  at(now - 26 * hour, 'Starting minecraft server version 26.1.2')
  at(now - 26 * hour + 9000, 'Done (8.214s)! For help, type "help"')
  for (const p of [...online].reverse()) at(p.since, `${p.name} joined the game`)
  const talk = [now - 9 * minute, now - 7 * minute, now - 5 * minute, now - 3 * minute, now - 90_000]
  talk.forEach((ms, i) => at(ms, fill(chatter[i + 7] ?? '', online.map((p) => p.name), i)))
  return lines.sort((a, b) => (a[0] ?? '').localeCompare(b[0] ?? '')).map(([ts, text], i) => ({ seq: i + 1, ts: ts ?? '', text: text ?? '' }))
}

function creativeLog(stoppedAt: number): LogLine[] {
  const texts = ['PixelPia left the game', 'Stopping the server', 'Saving players', 'Saving worlds', "Saving chunks for level 'ServerLevel[world]'/minecraft:overworld", 'ThreadedAnvilChunkStorage: All dimensions are saved']
  return texts.map((text, i) => ({ seq: i + 1, ts: iso(stoppedAt - 60_000 + i * 400), text: logText(stoppedAt - 60_000 + i * 400, text) }))
}

/** A chatter line with online players' names in it. */
export function fill(line: string, names: string[], turn: number): string {
  const pick = (k: number) => names[(turn + k) % Math.max(names.length, 1)] ?? 'Steve'
  return line.replace('{a}', pick(0)).replace('{b}', pick(1)).replace('{c}', pick(2))
}

// Requests to the demo's API, and what answers them.
export interface Request {
  params: Record<string, string>
  query: URLSearchParams
  body: unknown
  now: number
}
export type Handler = (s: DemoState, r: Request) => unknown
export type Routes = Record<string, Handler>

/** The server a path names, or the 404 the panel would give. */
export function serverOf(s: DemoState, r: Request): ServerStatus {
  const found = s.servers.find((x) => x.id === r.params.id)
  if (!found) throw new ApiError(404, { error: t('server.notFound'), code: 'not_found' })
  return found
}

const limit = (r: Request, fallback: number) => Number(r.query.get('limit') ?? fallback) || fallback

export function me(now: number): Me {
  return { user: { username: demoUser, role: 'owner' }, csrfToken: 'demo', expiresAt: iso(now + 12 * hour), idleTimeoutSeconds: 12 * 3600, version: demoVersion }
}

function logs(s: DemoState, r: Request): LogsResponse {
  const all = s.logs[r.params.id ?? ''] ?? []
  const after = Number(r.query.get('after') ?? 0)
  const lines = (r.query.has('after') ? all.filter((l) => l.seq > after) : all).slice(-limit(r, 200))
  return { epoch: `demo-${s.hour}`, lines, next: all.at(-1)?.seq ?? 0, truncated: false }
}

const ranges: Record<string, { span: number; bucket: number }> = {
  '24h': { span: day, bucket: 10 * minute },
  '7d': { span: 7 * day, bucket: hour },
  '30d': { span: 30 * day, bucket: 6 * hour },
}

function metrics(s: DemoState, r: Request): MetricsResponse {
  const srv = serverOf(s, r)
  const { span, bucket } = ranges[r.query.get('range') ?? ''] ?? { span: day, bucket: 10 * minute }
  const to = Math.floor(r.now / bucket) * bucket
  const from = to - span
  const since = Date.parse(srv.collectingSince ?? srv.createdAt)
  const stopped = srv.stoppedAt && srv.phase !== 'online' ? Date.parse(srv.stoppedAt) : Infinity
  const buckets: MetricsBucket[] = []
  for (let start = from; start < to; start += bucket) {
    if (start < since) {
      buckets.push({ start: iso(start), playersMax: null, cpuAvg: null, memAvg: null, coverage: 0, state: 'not_collected' })
      continue
    }
    if (start >= stopped) {
      buckets.push({ start: iso(start), playersMax: null, cpuAvg: null, memAvg: null, coverage: 1, state: 'offline' })
      continue
    }
    const base = busy[new Date(start).getHours()] ?? 0
    const players = Math.min(srv.config?.maxPlayers ?? 10, Math.max(0, base + Math.round(noise(start / bucket) * 2 - 0.8)))
    buckets.push({ start: iso(start), playersMax: players, cpuAvg: 6 + players * 4 + noise(start) * 5, memAvg: (1.9 + players * 0.12) * gb, coverage: 1, state: 'online' })
  }
  return { from: iso(from), to: iso(to), bucketSeconds: bucket / 1000, sampleIntervalSeconds: 60, buckets, gaps: stopped < to ? [{ from: iso(stopped), to: iso(to), kind: 'server_offline' }] : [], collectingSince: srv.collectingSince, source: 'rcon list' }
}

function sessions(s: DemoState, r: Request): SessionsResponse {
  const srv = serverOf(s, r)
  const live = s.live[srv.id]
  const list: Session[] = (live?.online ?? []).map((p, i) => ({ id: 100 + i, player: p.name, start: iso(p.since), startUncertain: false, endUncertain: false, durationSeconds: Math.round((r.now - p.since) / 1000), source: 'rcon list' }))
  const earlier: [string, number, number][] = [
    ['PixelPia', 6 * hour, 80],
    ['Brickbert', 9 * hour, 45],
    ['JunoFox', 20 * hour, 130],
    ['tobi2009', 21 * hour, 75],
  ]
  if (srv.id === survivalId) {
    earlier.forEach(([player, ago, mins], i) => {
      const start = r.now - ago
      list.push({ id: 10 + i, player, start: iso(start), end: iso(start + mins * minute), endReason: 'left', startUncertain: false, endUncertain: false, durationSeconds: mins * 60, source: 'rcon list' })
    })
  }
  return { from: iso(r.now - day), to: iso(r.now), sessions: list }
}

function summary(s: DemoState, r: Request): PlayersSummary {
  const srv = serverOf(s, r)
  const days = Math.min(Number(r.query.get('days') ?? 7) || 7, 90)
  const since = Date.parse(srv.collectingSince ?? srv.createdAt)
  const stopped = srv.phase === 'online' ? Infinity : Date.parse(srv.stoppedAt ?? '') || Infinity
  const list: DailyActivity[] = []
  for (let i = days - 1; i >= 0; i--) {
    const date = new Date(r.now - i * day)
    const ran = date.getTime() >= since && date.getTime() < stopped + day
    const unique = ran ? 2 + Math.round(noise(date.getDate() + srv.gamePort) * 3) : 0
    list.push({ date: date.toISOString().slice(0, 10), uniquePlayers: unique, sessions: unique * 2, playtimeSeconds: unique * (3000 + Math.round(noise(date.getDate()) * 4000)), playtimeLowerBound: false, playtimeUpperBound: false, coverage: ran ? 1 : 0 })
  }
  const onlineNow = new Set(s.live[srv.id]?.online.map((p) => p.name))
  const players = (s.roster[srv.id] ?? []).map((p) => ({ ...p, online: onlineNow.has(p.name), lastSeen: onlineNow.has(p.name) ? iso(r.now) : p.lastSeen }))
  return { tz: r.query.get('tz') ?? 'UTC', days: list, players, observedSessions: players.reduce((n, p) => n + p.sessions, 0), uncertainSessions: 0, collectingSince: srv.collectingSince, retentionDays: 90 }
}

export const versions: CatalogEntry[] = [
  { id: 'paper-26.2.1', label: '26.2.1', minecraftVersion: '26.2.1', paperBuild: 12, jarSha256: fakeSha(2621), java: 25, recommended: false, notes: '', channel: 'experimental', experimental: true, supported: true },
  { id: 'paper-26.1.2', label: '26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, jarSha256: fakeSha(2612), java: 25, recommended: true, notes: '', channel: 'default', experimental: false, supported: true },
  { id: 'paper-26.1.1', label: '26.1.1', minecraftVersion: '26.1.1', paperBuild: 51, jarSha256: fakeSha(2611), java: 25, recommended: false, notes: '', channel: 'default', experimental: false, supported: true },
  { id: 'paper-1.21.11', label: '1.21.11', minecraftVersion: '1.21.11', paperBuild: 130, jarSha256: fakeSha(12111), java: 21, recommended: false, notes: '', channel: 'default', experimental: false, supported: true },
]

function catalog(s: DemoState, r: Request): Catalog {
  const live = s.machine.live
  const total = live?.memoryTotalMB ?? 16384
  const reserve = live?.systemReserveMB ?? 1536
  const used = s.servers.reduce((n, x) => n + (x.config?.memoryMB ?? 0), 0)
  const ports = new Set(s.servers.map((x) => x.gamePort))
  let port = 25565
  while (ports.has(port)) port++
  const budgets = [1024, 2048, 3072, 4096, 6144, 8192]
  return {
    type: r.query.get('type') ?? 'paper',
    types: [{ id: 'paper', name: 'Paper', available: true }],
    versions,
    versionsCheckedAt: iso(r.now - 20 * minute),
    memoryOptionsMB: budgets,
    recommendedMemoryMB: 4096,
    hostMemoryMB: total,
    maxMemoryMB: total - reserve,
    systemReserveMB: reserve,
    memoryFreeMB: total - reserve - used,
    servers: s.servers.map((x) => ({ id: x.id, name: x.name, memoryMB: x.config?.memoryMB ?? 0, running: x.phase === 'online' })),
    suggestedPort: port,
    image,
    sizing: {
      workload: 'vanilla',
      budgets: budgets.map((memoryMB) => ({ memoryMB, heapMB: Math.round(memoryMB * 0.75), players: memoryMB < 2048 ? 0 : Math.round(memoryMB / 400) })),
      suggestions: [
        { players: 4, memoryMB: 2048 },
        { players: 10, memoryMB: 4096 },
        { players: 20, memoryMB: 6144 },
        { players: 40, memoryMB: 8192 },
      ],
    },
  }
}

export const update = (now: number): UpdateInfo => ({ current: demoVersion, supported: true, latest: demoVersion, available: false, checkedAt: iso(now - 30 * minute) })

function preflight(s: DemoState): Preflight {
  const live = s.machine.live
  return {
    ok: true,
    checks: [
      { id: 'os', label: 'System', status: 'pass', detail: live?.os ?? '' },
      { id: 'docker', label: 'Docker', status: 'pass', detail: `Docker ${live?.dockerVersion ?? ''} is running` },
      { id: 'memory', label: 'Memory', status: 'pass', detail: '16 GB, 7.5 GB not given to a server' },
      { id: 'disk', label: 'Disk', status: 'pass', detail: '41 GB free' },
    ],
  }
}

const link: MachineLinkInfo = {
  addresses: [
    { kind: 'name', address: 'demo.playkeeper.io:8443' },
    { kind: 'ip', address: '203.0.113.10:8443' },
  ],
  minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5 },
  sizingUrl: 'https://playkeeper.io/sizing',
  available: true,
  fingerprint: 'Z287KN4CDZD0Z8A4XXJA514NKG',
  codes: [],
}

/** GET paths the demo has sample data for. */
export const reads: Routes = {
  'GET /api/setup/status': () => ({ needsSetup: false }),
  'GET /api/auth/me': (_, r) => me(r.now),
  'GET /api/me/prefs': (s) => s.prefs,
  'GET /api/servers': (s) => s.servers,
  'GET /api/machines': (s) => [s.machine],
  'GET /api/machines/link': () => link,
  'GET /api/machines/:machine/activity': (s, r) => s.activity.slice(0, limit(r, 5)),
  'GET /api/machines/:machine/catalog': catalog,
  'GET /api/machines/:machine/update': (_, r) => update(r.now),
  'GET /api/machines/:machine/preflight': preflight,
  'GET /api/machines/:machine/events': () => [],
  'GET /api/audit': (s) => s.audit,
  'GET /api/tokens': (s) => s.tokens,
  'GET /api/tokens/activity': (s) => s.agentActivity,
  'GET /api/servers/:id/logs': logs,
  'GET /api/servers/:id/activity': (s, r) => s.activity.filter((a) => a.serverId === serverOf(s, r).id).slice(0, limit(r, 5)),
  'GET /api/servers/:id/metrics': metrics,
  'GET /api/servers/:id/players/sessions': sessions,
  'GET /api/servers/:id/players/summary': summary,
  'GET /api/servers/:id/whitelist': (s, r) => s.whitelist[serverOf(s, r).id] ?? [],
  'GET /api/servers/:id/operators': (s, r) => s.operators[serverOf(s, r).id] ?? [],
  'GET /api/servers/:id/backups': (s, r) => s.backups[serverOf(s, r).id] ?? [],
}
