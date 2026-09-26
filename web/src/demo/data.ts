// The live demo's sample data: a made-up VPS with two Paper servers and a
// Fabric one, their players, console, backups, plugins and mods, map
// pre-generation, packs, address, health and history.
// Every GET the dashboard makes is answered from `reads` at the bottom; a
// path that isn't there gets "no sample data" and its screen shows its empty
// state. To give a screen sample data, add what it needs to DemoState and
// sample(), and its path to reads.

import { ApiError } from '@/api/client'
import type {
  Action,
  Activity,
  Address,
  Addon,
  AddonBrowse,
  AddonCard,
  AddonChecks,
  AddonDetails,
  AddonFile,
  AddonRemovePreview,
  AddonSource,
  AddonSources,
  AddonVersion,
  Addons,
  AgentActivity,
  ApiToken,
  AuditEntry,
  Backup,
  Catalog,
  CatalogEntry,
  Crash,
  CuratedAddons,
  DailyActivity,
  DataPack,
  DataPacks,
  LogLine,
  LogsResponse,
  MachineLinkInfo,
  MachineView,
  Me,
  MemoryAdvice,
  MemoryFit,
  MetricsBucket,
  MetricsResponse,
  OperatorEntry,
  PackShare,
  Phase,
  PlayerStat,
  PlayersSummary,
  Preflight,
  Pregen,
  PregenPreset,
  PregenPresetId,
  ResourcePack,
  ResourcePackOffer,
  Running,
  ServerConfig,
  ServerStatus,
  Session,
  SessionsResponse,
  ShareMod,
  ShareNeed,
  SoftwareBuilds,
  SoftwarePin,
  TwoFactorStatus,
  UpdateInfo,
  WhitelistEntry,
} from '@/api/types'
import { t } from '@/i18n'
import { typeName } from '@/lib/servers'
import { addonKind } from '@/lib/software'
import { faceCount, faceIndex } from './faces'

/** Bump when DemoState changes shape, so sessions saved by an older demo start over. */
export const sampleVersion = 3
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
  addons: Record<string, ServerAddons>
  pregen: Record<string, PregenTask>
  packs: Record<string, ServerPacks>
}

/** A library plugin on a server, at the version Playkeeper installed. */
export interface InstalledAddon {
  source: AddonSource
  projectId: string
  version: string
  /** When that version came out, and when it went in the plugins folder. */
  published: number
  installedAt: number
}

/** A jar someone put in the plugins folder themselves, as its plugin.yml names it. */
export interface HandAddon {
  fileName: string
  name: string
  version: string
  size: number
}

export interface ServerAddons {
  installed: InstalledAddon[]
  byHand: HandAddon[]
}

/** A pre-generation of the map, which runs at a steady rate while its server is online. */
export interface PregenTask {
  preset: PregenPresetId
  radius: number
  total: number
  /** Chunks a second. */
  rate: number
  startedAt: number
  pauseForPlayers: boolean
}

export interface ServerPacks {
  /** The resource pack players are offered; its address comes from the one the demo is open at. */
  resource?: Omit<ResourcePackOffer, 'url'>
  data: DataPack[]
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
const cobblemonId = 'k9p4f7x2ne'

/** The machine's free name; playkeeper.io reserves "demo", so it leads to nobody's server. */
const freeName = 'demo'
const machineIP = '203.0.113.10'

export function config(over: Partial<ServerConfig> & Pick<ServerConfig, 'minecraftVersion' | 'memoryMB' | 'motd' | 'createdAt'>): ServerConfig {
  const type = over.type ?? 'paper'
  return {
    type,
    versionId: `${type}-${over.minecraftVersion}`,
    paperBuild: type === 'paper' ? 74 : 0,
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

const slugs: Record<string, string> = { [survivalId]: 'survival', [creativeId]: 'creative', [cobblemonId]: 'cobblemon' }

function backup(serverId: string, n: number, at: number, sizeBytes: number, over: Partial<Backup> = {}): Backup {
  const stamp = iso(at).slice(0, 16).replace(/[-:T]/g, '')
  return {
    id: `bk${serverId.slice(0, 4)}${n}`,
    serverId,
    kind: 'manual',
    createdAt: iso(at),
    fileName: `${slugs[serverId] ?? 'server'}-${stamp}.tar.gz`,
    sizeBytes,
    sha256: fakeSha(at % 100_000),
    location: 'local',
    verified: true,
    verifiedAt: iso(at + 2 * minute),
    downtimeMs: 0,
    method: 'online_copy',
    savingPausedMs: 1_400 + (at % 900),
    durationMs: 9_000 + (at % 7_000),
    minecraftVersion: serverId === creativeId ? '26.2.1' : '26.1.2',
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
    joinAddress: `survival.${freeName}.playkeeper.io`,
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
    joinAddress: `creative.${freeName}.playkeeper.io`,
  }
  const cobblemonCreated = now - 9 * day
  const crashedAt = now - 34 * minute
  const cobblemonBackups = [backup(cobblemonId, 1, now - day - 7 * hour, 0.81 * gb)]
  const cobblemon: ServerStatus = {
    id: cobblemonId,
    name: 'Cobblemon',
    slug: 'cobblemon',
    game: 'minecraft-java',
    type: 'fabric',
    createdAt: iso(cobblemonCreated),
    machineId,
    exists: true,
    desired: 'running',
    phase: 'crashed',
    reachable: false,
    stoppedAt: iso(crashedAt),
    config: config({
      minecraftVersion: '26.1.2',
      memoryMB: 4096,
      motd: 'Catch them with friends',
      createdAt: iso(cobblemonCreated),
      playStyle: 'friends',
      type: 'fabric',
      software: pinOf('fabric', '26.1.2'),
    }),
    gameplay: { difficulty: 'normal', pvp: false, gameMode: 'survival', hardcore: false, viewDistance: 10, levelType: 'normal' },
    gamePort: 25567,
    offlineModeTest: false,
    crashCount: 1,
    crash: outOfMemory(crashedAt),
    lastBackup: cobblemonBackups[0],
    worldBytes: 0.86 * gb,
    pendingRestart: false,
    collectingSince: iso(cobblemonCreated),
    firstSteps: { invited: 'Brickbert', friendJoined: 'Brickbert', friendJoinedAt: iso(cobblemonCreated + 2 * hour), backedUp: true, downloaded: true },
    joinAddress: `cobblemon.${freeName}.playkeeper.io`,
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
        memoryTotalMB: 24576,
        systemReserveMB: 1536,
        serversMemoryMB: 11264,
        memoryFreeMB: 11776,
        diskFreeBytes: 41 * gb,
        diskTotalBytes: 80 * gb,
        docker: true,
        dockerVersion: '28.4.0',
        agentVersion: demoVersion,
        defaultGamePort: 25565,
        offlineModeTest: false,
        servers: 3,
      },
    },
    servers: [survival, creative, cobblemon],
    live: {
      [survivalId]: { online, joins: [], away: [], nextLine: now + 4000, cursor: 0 },
      [creativeId]: { online: [], joins: [], away: ['PixelPia'], nextLine: now, cursor: 0 },
      [cobblemonId]: { online: [], joins: [], away: ['Brickbert', 'Kestrel_7', 'mara_k', 'tobi2009'], nextLine: now, cursor: 0 },
    },
    backups: { [survivalId]: survivalBackups, [creativeId]: creativeBackups, [cobblemonId]: cobblemonBackups },
    logs: { [survivalId]: survivalLog(now, online), [creativeId]: creativeLog(stoppedAt), [cobblemonId]: cobblemonLog(crashedAt) },
    whitelist: {
      [survivalId]: samplePlayers.map((name) => ({ name })),
      [creativeId]: ['PixelPia', 'Brickbert', 'JunoFox'].map((name) => ({ name })),
      [cobblemonId]: ['Brickbert', 'Kestrel_7', 'mara_k', 'tobi2009'].map((name) => ({ name })),
    },
    operators: { [survivalId]: [{ name: 'JunoFox', level: 4 }], [creativeId]: [{ name: 'PixelPia', level: 4 }], [cobblemonId]: [{ name: 'Brickbert', level: 4 }] },
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
      [cobblemonId]: [
        stat('Brickbert', crashedAt, 14, 22.5),
        stat('Kestrel_7', crashedAt, 9, 13.1),
        stat('mara_k', crashedAt, 6, 7.4),
        stat('tobi2009', crashedAt, 5, 6.2),
      ],
    },
    activity: [
      { ts: iso(now - 12 * minute), serverId: survivalId, kind: 'joined', player: 'JunoFox' },
      { ts: iso(now - 48 * minute), serverId: survivalId, kind: 'joined', player: 'tobi2009' },
      { ts: iso(now - 62 * minute), serverId: survivalId, kind: 'backup', actor: demoUser },
      { ts: iso(crashedAt), serverId: cobblemonId, kind: 'crashed' },
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
    addons: {
      [survivalId]: {
        installed: [
          // The pre-generation put Chunky in just before it started.
          install(now, 'chunky', now - 21 * minute - 40_000),
          install(now, 'luckperms', now - 29 * day, { version: '5.5.10', published: now - 30 * day }),
          install(now, 'coreprotect', created + 2 * day),
          install(now, 'bluemap', now - 17 * day),
          install(now, 'ViaVersion', now - 8 * day),
        ],
        byHand: [{ fileName: 'FriendsWelcome.jar', name: 'FriendsWelcome', version: '1.0', size: 14_336 }],
      },
      [creativeId]: { installed: [install(now, 'worldedit', creativeCreated + hour)], byHand: [] },
      [cobblemonId]: {
        installed: [install(now, 'fabric-api', cobblemonCreated + hour), install(now, 'cobblemon', cobblemonCreated + hour), install(now, 'lithium', cobblemonCreated + day)],
        byHand: [],
      },
    },
    pregen: {
      [survivalId]: { preset: 'medium', radius: 2500, total: squareChunks(2500), rate: 27.5, startedAt: now - 21 * minute, pauseForPlayers: false },
    },
    packs: {
      [survivalId]: {
        resource: { sha1: fakeSha(3201).slice(0, 40), fileName: 'Cosy_Blocks_32x.zip', size: 19_480_000, description: 'Softer, warmer blocks for a friendly survival world', addedAt: iso(now - 9 * day), required: false },
        data: [
          { name: 'Coordinates_HUD.zip', description: 'Shows where you are above the hotbar', size: 24_576, enabled: true, addedAt: iso(created + 3 * day) },
          { name: 'Multiplayer_Sleep.zip', description: 'Skips the night once half the players sleep', size: 18_432, enabled: true, addedAt: iso(created + 3 * day) },
          { name: 'More_Mob_Heads.zip', description: 'Mobs sometimes drop their heads', size: 1_310_720, enabled: false, addedAt: iso(now - 12 * day) },
        ],
      },
      [creativeId]: { data: [] },
      [cobblemonId]: { data: [] },
    },
  }
}

/** Cobblemon's crash: out of memory with four players on, and room on the machine for more. */
function outOfMemory(at: number): Crash {
  return {
    at: iso(at),
    start: false,
    kind: 'heap_out_of_memory',
    params: { budget_mb: 4096, heap_mb: 3072, players: 4 },
    certain: true,
    title: 'It ran out of memory',
    explanation: 'Java used all 3 GB it may use, with 4 players on, and the server stopped.',
    evidence: [{ kind: 'log', text: 'java.lang.OutOfMemoryError: Java heap space' }],
    fixes: [
      { kind: 'raise_memory', params: { to_mb: 6144 }, title: 'Give it 6 GB', recommended: true },
      { kind: 'restart', title: 'Start it again' },
    ],
    lines: [
      { time: clock(at - 9_000), level: 'WARN', text: "Can't keep up! Is the server overloaded? Running 5230ms or 104 ticks behind" },
      { time: clock(at - 2_000), level: 'ERROR', text: 'Encountered an unexpected exception' },
      { time: clock(at), level: 'FATAL', text: 'java.lang.OutOfMemoryError: Java heap space' },
    ],
    roomMB: 11776,
  }
}

function clock(at: number): string {
  return logText(at, '').slice(1, 9)
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

function cobblemonLog(crashedAt: number): LogLine[] {
  const lines: [number, string, string][] = [
    [crashedAt - 70_000, 'Brickbert joined the game', 'INFO'],
    [crashedAt - 9_000, "Can't keep up! Is the server overloaded? Running 5230ms or 104 ticks behind", 'WARN'],
    [crashedAt - 2_000, 'Encountered an unexpected exception', 'ERROR'],
    [crashedAt, 'java.lang.OutOfMemoryError: Java heap space', 'FATAL'],
  ]
  return lines.map(([at, text, level], i) => ({ seq: i + 1, ts: iso(at), text: logText(at, text, level) }))
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

/** Everything an owner may do, as the panel's permit lists it for them. */
const ownerCan: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']

export function me(now: number): Me {
  return {
    user: { username: demoUser, role: 'owner' },
    csrfToken: 'demo',
    expiresAt: iso(now + 12 * hour),
    idleTimeoutSeconds: 12 * 3600,
    version: demoVersion,
    access: { role: 'admin', servers: { all: true }, twoFactor: false, can: ownerCan },
  }
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
      buckets.push({ start: iso(start), playersMax: null, cpuAvg: null, memAvg: null, tpsAvg: null, msptAvg: null, coverage: 0, state: 'not_collected' })
      continue
    }
    if (start >= stopped) {
      buckets.push({ start: iso(start), playersMax: null, cpuAvg: null, memAvg: null, tpsAvg: null, msptAvg: null, coverage: 1, state: 'offline' })
      continue
    }
    const base = busy[new Date(start).getHours()] ?? 0
    const players = Math.min(srv.config?.maxPlayers ?? 10, Math.max(0, base + Math.round(noise(start / bucket) * 2 - 0.8)))
    buckets.push({ start: iso(start), playersMax: players, cpuAvg: 6 + players * 4 + noise(start) * 5, memAvg: (1.9 + players * 0.12) * gb, tpsAvg: 20, msptAvg: 14 + players * 2.5 + noise(start + 1) * 6, coverage: 1, state: 'online' })
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

const serverTypes = [
  { id: 'paper', name: 'Paper' },
  { id: 'purpur', name: 'Purpur' },
  { id: 'fabric', name: 'Fabric' },
  { id: 'quilt', name: 'Quilt' },
  { id: 'neoforge', name: 'NeoForge' },
  { id: 'vanilla', name: 'Vanilla' },
]

/** The builds a type offers for a Minecraft version, newest and recommended first; Paper and Vanilla have none. */
export function buildsFor(type: string, minecraftVersion: string): string[] {
  switch (type) {
    case 'purpur':
      return ['2461', '2460', '2457']
    case 'fabric':
      return ['0.19.3', '0.19.2', '0.18.4']
    case 'quilt':
      return ['0.30.1', '0.30.0']
    case 'neoforge':
      return [`${minecraftVersion}.18`, `${minecraftVersion}.11`]
    default:
      return []
  }
}

/** A server's pin on a type and Minecraft version, at the given build or the recommended one. */
export function pinOf(type: string, minecraftVersion: string, build = buildsFor(type, minecraftVersion)[0]): SoftwarePin {
  switch (type) {
    case 'purpur':
      return { type, minecraftVersion, purpurBuild: Number(build) }
    case 'fabric':
      return { type, minecraftVersion, fabricLoader: build }
    case 'quilt':
      return { type, minecraftVersion, quiltLoader: build }
    case 'neoforge':
      return { type, minecraftVersion, neoforgeVersion: build }
    default:
      return { type, minecraftVersion }
  }
}

/** The Minecraft versions a type offers: Paper's builds, or the same versions pinned to the type's recommended build. */
export function versionsOf(type: string): CatalogEntry[] {
  if (type === 'paper') return versions
  return versions.map((v) => ({ ...v, id: `${type}-${v.minecraftVersion}`, paperBuild: 0, software: pinOf(type, v.minecraftVersion), build: buildsFor(type, v.minecraftVersion)[0] }))
}

function builds(_s: DemoState, r: Request): SoftwareBuilds {
  const type = r.query.get('type') ?? ''
  const version = r.query.get('version') ?? ''
  return { type, minecraftVersion: version, builds: buildsFor(type, version).map((build, i) => ({ version: build, channel: 'stable', recommended: i === 0 })), checkedAt: iso(r.now - 20 * minute) }
}

function catalog(s: DemoState, r: Request): Catalog {
  const asked = s.servers.find((x) => x.id === r.query.get('server'))?.type ?? r.query.get('type')
  const type = asked && serverTypes.some((x) => x.id === asked) ? asked : 'paper'
  const live = s.machine.live
  const total = live?.memoryTotalMB ?? 24576
  const reserve = live?.systemReserveMB ?? 1536
  const used = s.servers.reduce((n, x) => n + (x.config?.memoryMB ?? 0), 0)
  const ports = new Set(s.servers.map((x) => x.gamePort))
  let port = 25565
  while (ports.has(port)) port++
  const budgets = [1024, 2048, 3072, 4096, 6144, 8192]
  return {
    type,
    types: serverTypes.map((x) => ({ ...x, available: true })),
    versions: versionsOf(type),
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
      { id: 'memory', label: 'Memory', status: 'pass', detail: '24 GB, 11.5 GB not given to a server' },
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

// Plugins, the map's pre-generation, and packs.

/** A plugin as its library lists it, with its newest version. */
interface LibraryAddon {
  source: AddonSource
  projectId: string
  slug: string
  /** Hangar's owner, which its page's address starts with. */
  owner?: string
  name: string
  author: string
  summary: string
  categories: string[]
  license: string
  downloads: number
  version: string
  /** How many days ago the newest version came out, and what its page says about it. */
  daysOld: number
  notes: string
  /** The jar's name, with {v} for the version. */
  jar: string
  size: number
  /** Its drawing in the demo, and the folder of plugins/ its settings live in. */
  icon: number
  folder: string
  /** A mod, for Fabric servers; plugins otherwise. */
  mod?: true
  /** What friends need of a mod. */
  need?: ShareNeed
  /** The "Picked by Playkeeper" entry it fills. */
  pick?: string
}

/** Every plugin and mod the demo's library has: the sample servers' own, and more to browse. */
export const library: LibraryAddon[] = [
  { source: 'modrinth', projectId: 'fALzjamp', slug: 'chunky', name: 'Chunky', author: 'pop4959', summary: 'Pre-generates chunks, quickly and efficiently', categories: ['world', 'optimization'], license: 'GPL-3.0-only', downloads: 2_830_000, version: '1.5.3', daysOld: 12, notes: 'Generates faster on Paper and reports progress more often.', jar: 'Chunky-Bukkit-{v}.jar', size: 304_616, icon: 0, folder: 'Chunky', pick: 'pregenerate' },
  { source: 'modrinth', projectId: 'Vebnzrzj', slug: 'luckperms', name: 'LuckPerms', author: 'Luck', summary: 'A permissions plugin for Minecraft servers', categories: ['admin'], license: 'MIT', downloads: 4_260_000, version: '5.5.11', daysOld: 2, notes: 'Fixes the web editor’s link on servers behind a proxy, and updates translations.', jar: 'LuckPerms-Bukkit-{v}.jar', size: 1_734_000, icon: 1, folder: 'LuckPerms', pick: 'permissions' },
  { source: 'modrinth', projectId: 'Lu3KuzdV', slug: 'coreprotect', name: 'CoreProtect', author: 'Intelli', summary: 'Fast, efficient block logging, rollbacks and restores', categories: ['admin', 'protection'], license: 'Artistic-2.0', downloads: 1_870_000, version: '23.1', daysOld: 41, notes: 'Supports Minecraft 26.1 and logs item frames again.', jar: 'CoreProtect-{v}.jar', size: 1_062_000, icon: 2, folder: 'CoreProtect', pick: 'rollback' },
  { source: 'modrinth', projectId: 'swbUV1cr', slug: 'bluemap', name: 'BlueMap', author: 'Blue', summary: 'A 3D map of your world that players open in a browser', categories: ['world', 'utility'], license: 'MIT', downloads: 1_120_000, version: '5.13', daysOld: 18, notes: 'Renders 26.1’s new blocks and opens large maps faster.', jar: 'bluemap-{v}-paper.jar', size: 3_950_000, icon: 3, folder: 'BlueMap' },
  { source: 'hangar', projectId: 'ViaVersion', slug: 'ViaVersion', owner: 'ViaVersion', name: 'ViaVersion', author: 'ViaVersion', summary: 'Lets players on newer Minecraft versions join your server', categories: ['utility', 'library'], license: 'GPL-3.0', downloads: 6_140_000, version: '5.5.1', daysOld: 9, notes: 'Lets 26.2 players join.', jar: 'ViaVersion-{v}.jar', size: 5_380_000, icon: 4, folder: 'ViaVersion', pick: 'newer-clients' },
  { source: 'modrinth', projectId: '1u6JkXh5', slug: 'worldedit', name: 'WorldEdit', author: 'EngineHub', summary: 'An in-game map editor for building and shaping land', categories: ['world', 'utility'], license: 'GPL-3.0-only', downloads: 7_950_000, version: '7.3.17', daysOld: 23, notes: 'Knows the blocks added in Minecraft 26.2.', jar: 'worldedit-bukkit-{v}.jar', size: 4_120_000, icon: 5, folder: 'WorldEdit' },
  { source: 'hangar', projectId: 'Geyser', slug: 'Geyser', owner: 'GeyserMC', name: 'Geyser', author: 'GeyserMC', summary: 'Lets Bedrock players on phones and consoles join your Java server', categories: ['utility'], license: 'MIT', downloads: 3_480_000, version: '2.9.0', daysOld: 4, notes: 'Supports the latest Bedrock release.', jar: 'Geyser-Spigot-{v}.jar', size: 17_400_000, icon: 6, folder: 'Geyser-Spigot' },
  { source: 'hangar', projectId: 'Essentials', slug: 'Essentials', owner: 'EssentialsX', name: 'EssentialsX', author: 'EssentialsX', summary: 'Homes, warps, kits and the everyday commands most servers want', categories: ['admin', 'utility'], license: 'GPL-3.0', downloads: 2_290_000, version: '2.21.2', daysOld: 33, notes: 'Fixes /home on servers with several worlds.', jar: 'EssentialsX-{v}.jar', size: 3_210_000, icon: 7, folder: 'Essentials', pick: 'essentials' },
  { source: 'modrinth', projectId: 'l6YH9Als', slug: 'spark', name: 'spark', author: 'lucko', summary: 'Finds out what makes your server lag', categories: ['optimization', 'utility'], license: 'GPL-3.0-only', downloads: 5_320_000, version: '1.10.142', daysOld: 6, notes: 'Samples Paper’s new chunk system.', jar: 'spark-{v}-bukkit.jar', size: 2_060_000, icon: 8, folder: 'spark', pick: 'lag-finder' },
  { source: 'modrinth', projectId: '3wmN97b8', slug: 'multiverse-core', name: 'Multiverse-Core', author: 'Multiverse', summary: 'Adds more worlds to one server, each with its own settings', categories: ['world', 'admin'], license: 'BSD-3-Clause', downloads: 1_540_000, version: '5.3.1', daysOld: 27, notes: 'Keeps each world’s game rules when it is loaded again.', jar: 'multiverse-core-{v}.jar', size: 1_380_000, icon: 9, folder: 'Multiverse-Core' },
  { source: 'modrinth', projectId: 'TsLS8Py5', slug: 'skinsrestorer', name: 'SkinsRestorer', author: 'SkinsRestorer', summary: 'Keeps players’ skins and lets them pick new ones', categories: ['utility'], license: 'GPL-3.0-only', downloads: 1_010_000, version: '15.8.2', daysOld: 15, notes: 'Loads skins faster when many players join at once.', jar: 'SkinsRestorer-{v}.jar', size: 2_540_000, icon: 10, folder: 'SkinsRestorer' },
  { source: 'modrinth', projectId: 'UmLGoGij', slug: 'discordsrv', name: 'DiscordSRV', author: 'Scarsz', summary: 'Links your server’s chat to a Discord channel', categories: ['chat'], license: 'GPL-3.0-only', downloads: 860_000, version: '1.30.1', daysOld: 38, notes: 'Shows players’ faces next to their messages again.', jar: 'DiscordSRV-Build-{v}.jar', size: 9_870_000, icon: 11, folder: 'DiscordSRV' },
  { source: 'modrinth', projectId: 'P7dR8mSH', slug: 'fabric-api', name: 'Fabric API', author: 'modmuss50', summary: 'The hooks most Fabric mods need to work', categories: ['library'], license: 'Apache-2.0', downloads: 131_000_000, version: '0.140.2', daysOld: 5, notes: 'Supports Minecraft 26.1.2.', jar: 'fabric-api-{v}.jar', size: 2_310_000, icon: 13, folder: 'fabric', mod: true, need: 'required' },
  { source: 'modrinth', projectId: 'MdwFAVRL', slug: 'cobblemon', name: 'Cobblemon', author: 'Cobblemon', summary: 'Catch, train and battle creatures across the world', categories: ['adventure', 'mobs'], license: 'MPL-2.0', downloads: 9_800_000, version: '1.7.1', daysOld: 11, notes: 'Adds new creatures and fixes trading between players.', jar: 'Cobblemon-fabric-{v}.jar', size: 88_400_000, icon: 14, folder: 'cobblemon', mod: true, need: 'required' },
  { source: 'modrinth', projectId: 'gvQqBUqZ', slug: 'lithium', name: 'Lithium', author: 'CaffeineMC', summary: 'Makes the game run faster without changing how it plays', categories: ['optimization'], license: 'LGPL-3.0-only', downloads: 48_600_000, version: '0.18.1', daysOld: 19, notes: 'Speeds up mob pathfinding.', jar: 'lithium-fabric-{v}.jar', size: 740_000, icon: 15, folder: 'lithium', mod: true, need: 'optional' },
]

/** The library's categories, in the order the dashboard lists them. */
const addonCategories = ['admin', 'chat', 'economy', 'gameplay', 'minigames', 'world', 'protection', 'optimization', 'utility', 'library', 'adventure', 'technology', 'magic', 'storage', 'mobs']

function iconUrlOf(a: LibraryAddon): string {
  return a.source === 'modrinth' ? `https://cdn.modrinth.com/data/${a.projectId}/icon.png` : `https://hangarcdn.papermc.io/avatars/project/${a.icon + 1}.webp`
}

/** The demo's drawing for an add-on icon's address, when the add-on is one of the library's. */
export function addonIconOf(url: string): number | undefined {
  return library.find((a) => iconUrlOf(a) === url)?.icon
}

/** A library plugin at installedAt: its newest version, or an older one that came out at `older.published`. */
function install(now: number, slug: string, installedAt: number, older?: { version: string; published: number }): InstalledAddon {
  const a = library.find((x) => x.slug === slug)
  if (!a) throw new Error(`the demo's library has no ${slug}`)
  return { source: a.source, projectId: a.projectId, version: older?.version ?? a.version, published: older?.published ?? now - a.daysOld * day, installedAt }
}

function libraryAddon(source: string | undefined, projectId: string | undefined): LibraryAddon | undefined {
  return library.find((a) => a.source === source && a.projectId === projectId)
}

/** A server's library plugins, each with its library entry. */
function installedOn(s: DemoState, serverId: string): { a: LibraryAddon; inst: InstalledAddon }[] {
  return (s.addons[serverId]?.installed ?? []).flatMap((inst) => {
    const a = libraryAddon(inst.source, inst.projectId)
    return a ? [{ a, inst }] : []
  })
}

function versionOf(a: LibraryAddon, version: string, published: number): AddonVersion {
  const versionId = a.source === 'modrinth' ? fakeSha(published % 1_000_000).slice(0, 8) : version
  return { versionId, versionNumber: version, channel: 'release', published: iso(published), fileName: a.jar.replace('{v}', version), size: a.size }
}

const latestOf = (a: LibraryAddon, now: number) => versionOf(a, a.version, now - a.daysOld * day)

function record(a: LibraryAddon, inst: InstalledAddon): Addon {
  const v = versionOf(a, inst.version, inst.published)
  return { source: a.source, projectId: a.projectId, slug: a.slug, name: a.name, summary: a.summary, iconUrl: iconUrlOf(a), versionId: v.versionId, versionNumber: v.versionNumber, channel: v.channel, published: v.published, fileName: v.fileName ?? '', size: a.size, installedAt: iso(inst.installedAt) }
}

function card(a: LibraryAddon, now: number, installed: boolean): AddonCard {
  const pageUrl = a.source === 'modrinth' ? `https://modrinth.com/${a.mod ? 'mod' : 'plugin'}/${a.slug}` : `https://hangar.papermc.io/${a.owner ?? a.slug}/${a.slug}`
  return { source: a.source, projectId: a.projectId, slug: a.slug, name: a.name, author: a.author, summary: a.summary, categories: a.categories, license: a.license, downloads: a.downloads, iconUrl: iconUrlOf(a), updated: iso(now - a.daysOld * day), pageUrl, installed }
}

/** Whether a server takes mods (Fabric, Quilt, NeoForge) or plugins (Paper, Purpur). */
const takesMods = (srv: ServerStatus) => addonKind(srv.type) === 'mods'

/** The library's plugins or mods, whichever the server takes. */
const libraryFor = (srv: ServerStatus) => library.filter((a) => !!a.mod === takesMods(srv))

function addons(s: DemoState, r: Request): Addons {
  const srv = serverOf(s, r)
  const files: AddonFile[] = installedOn(s, srv.id).map(({ a, inst }) => {
    const addon = record(a, inst)
    return { fileName: addon.fileName, size: addon.size, status: 'managed', addon }
  })
  for (const h of s.addons[srv.id]?.byHand ?? []) files.push({ fileName: h.fileName, size: h.size, status: 'unknown', name: h.name, version: h.version })
  return {
    target: takesMods(srv)
      ? { kind: 'mod', folder: 'mods', sources: ['modrinth'], categories: addonCategories, minecraftVersion: srv.config?.minecraftVersion ?? '' }
      : { kind: 'plugin', folder: 'plugins', sources: ['modrinth', 'hangar'], categories: addonCategories, minecraftVersion: srv.config?.minecraftVersion ?? '' },
    files,
    missing: [],
    warnings: [],
    restartNeeded: false,
  }
}

function addonChecks(s: DemoState, r: Request): AddonChecks {
  const srv = serverOf(s, r)
  const updates = installedOn(s, srv.id).map(({ a, inst }) => ({ source: a.source, projectId: a.projectId, latest: latestOf(a, r.now), available: inst.version !== a.version }))
  return { updates, identified: [], checkedAt: iso(r.now - 20 * minute) }
}

const words = (text: string) => text.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '')

function addonSearch(s: DemoState, r: Request): AddonBrowse {
  const srv = serverOf(s, r)
  const q = words(r.query.get('q') ?? '')
  const category = r.query.get('category') ?? ''
  const mine = new Set(installedOn(s, srv.id).map(({ a }) => a))
  const found = libraryFor(srv).filter((a) => (!q || [a.name, a.summary, a.author].some((x) => words(x).includes(q))) && (!category || a.categories.includes(category)))
  const byDownloads = (x: LibraryAddon, y: LibraryAddon) => y.downloads - x.downloads
  switch (r.query.get('sort')) {
    case 'updated':
      found.sort((x, y) => x.daysOld - y.daysOld)
      break
    case 'relevance':
      found.sort((x, y) => Number(words(y.name).startsWith(q)) - Number(words(x.name).startsWith(q)) || byDownloads(x, y))
      break
    default:
      found.sort(byDownloads)
  }
  const first = !Number(r.query.get('page') ?? 0)
  return { cards: first ? found.map((a) => card(a, r.now, mine.has(a))) : [], more: false, unanswered: [] }
}

function addonDetails(s: DemoState, r: Request): AddonDetails {
  const srv = serverOf(s, r)
  const a = libraryAddon(r.params.source, r.params.project)
  if (!a) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  const inst = installedOn(s, srv.id).find((x) => x.a === a)?.inst
  const latest = latestOf(a, r.now)
  const out: AddonDetails = { card: card(a, r.now, !!inst), latest, notes: a.notes }
  if (inst) return { ...out, installed: record(a, inst), updateAvailable: inst.version !== a.version }
  const step = { action: 'install' as const, source: a.source, projectId: a.projectId, name: a.name, versionNumber: a.version, channel: 'release', fileName: latest.fileName ?? '', size: a.size }
  return { ...out, plan: { steps: [step], manual: [], blockers: [], warnings: [], ready: true, fingerprint: fakeSha(a.icon + 1).slice(0, 32) } }
}

function addonRemoval(s: DemoState, r: Request): AddonRemovePreview {
  const srv = serverOf(s, r)
  const found = installedOn(s, srv.id).find(({ a }) => a.source === r.params.source && a.projectId === r.params.project)
  if (!found) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  return { addon: record(found.a, found.inst), neededBy: [], orphans: [], configFolder: `${found.a.mod ? 'config' : 'plugins'}/${found.a.folder}`, changed: false, missing: false }
}

/** The chunks in a square this far out from spawn, as Chunky counts them. */
export function squareChunks(radius: number): number {
  return (2 * Math.ceil(radius / 16) + 1) ** 2
}

/** What a generated chunk takes on disk, in the middle of Playkeeper's estimate. */
const chunkBytes = 9.82 * 1024 * 1.05

const pregenPresets: [PregenPresetId, number][] = [
  ['small', 1_000],
  ['medium', 2_500],
  ['large', 5_000],
  ['huge', 10_000],
]

/** The sizes on offer: at the rate the last task ran, or else as long as the agent estimates for this machine's cores. */
function presets(s: DemoState, rate?: number): PregenPreset[] {
  const cpus = s.machine.live?.cpus ?? 4
  const free = s.machine.live?.diskFreeBytes ?? 0
  return pregenPresets.map(([id, radius]) => {
    const chunks = squareChunks(radius)
    const seconds = rate ? chunks / rate : Math.sqrt((chunks / (12 * cpus)) * (chunks / (4 * cpus)))
    const diskBytes = Math.round(chunks * chunkBytes)
    return { id, radius, chunks, seconds: Math.round(seconds), diskBytes, fits: diskBytes * 1.4 < free }
  })
}

function pregen(s: DemoState, r: Request): Pregen {
  const srv = serverOf(s, r)
  const task = s.pregen[srv.id]
  const chunky = installedOn(s, srv.id).some(({ a }) => a.slug === 'chunky')
  const world = srv.config?.levelName ?? 'world'
  const diskFreeBytes = s.machine.live?.diskFreeBytes
  if (!task) return { state: 'idle', world, chunks: 0, total: 0, percent: 0, etaSeconds: -1, pauseForPlayers: true, installed: chunky, presets: presets(s), diskFreeBytes }
  const base = { world, preset: task.preset, radius: task.radius, total: task.total, pauseForPlayers: task.pauseForPlayers, startedAt: iso(task.startedAt), installed: true, presets: presets(s, task.rate), diskFreeBytes }
  // It runs while the server is online, and stops where it was when the server stopped.
  const online = srv.phase === 'online'
  const until = online ? r.now : Math.min(r.now, Date.parse(srv.stoppedAt ?? '') || r.now)
  const took = (task.total / task.rate) * 1000
  if (until >= task.startedAt + took) {
    return { ...base, state: 'finished', chunks: task.total, percent: 100, etaSeconds: -1, elapsedSeconds: Math.round(took / 1000), finishedAt: iso(task.startedAt + took), diskBytes: Math.round(task.total * chunkBytes) }
  }
  const chunks = Math.floor((Math.max(0, until - task.startedAt) / 1000) * task.rate)
  const percent = (chunks / task.total) * 100
  if (!online) return { ...base, state: 'paused', pausedBy: 'server', chunks, percent, etaSeconds: -1, elapsedSeconds: Math.round(chunks / task.rate) }
  return { ...base, state: 'running', chunks, percent, rate: task.rate, etaSeconds: Math.round((task.total - chunks) / task.rate) }
}

function resourcePack(s: DemoState, r: Request): ResourcePack {
  const offer = s.packs[serverOf(s, r).id]?.resource
  if (!offer) return { pending: false }
  // Players' games download the pack from the address the dashboard is open at,
  // which the demo build names demo.playkeeper.io, as it does game addresses.
  const host = typeof window === 'undefined' ? 'demo.playkeeper.io' : window.location.hostname
  return { offer: { ...offer, url: `http://${host}:8443/resource-packs/${offer.sha1}.zip` }, pending: false }
}

function dataPacks(s: DemoState, r: Request): DataPacks {
  const srv = serverOf(s, r)
  // Only a running server says which of its packs are on.
  const live = srv.phase === 'online'
  const packs = (s.packs[srv.id]?.data ?? []).map((p) => (live ? p : { ...p, enabled: undefined }))
  return { packs, live }
}

/** GET paths the demo has sample data for. */
// Machine settings › Address and Account's two-factor sign-in.

function address(s: DemoState, r: Request): Address {
  const claimed = r.now - 32 * day
  const host = `${freeName}.playkeeper.io`
  return {
    kind: 'playkeeper',
    host,
    since: iso(claimed),
    ip: machineIP,
    panelPort: 8443,
    base: 'playkeeper.io',
    servers: s.servers.map((x) => ({ serverId: x.id, name: x.name, port: x.gamePort, label: x.slug, address: `${x.slug}.${host}`, direct: `${machineIP}:${x.gamePort}`, published: true })),
    free: { name: freeName, state: 'active', dns: 'ok', ipv4: machineIP, claimedAt: iso(claimed), refreshedAt: iso(r.now - 3 * hour), checkedAt: iso(r.now - 2 * hour), holdDays: 30 },
    certificate: { names: [host], challenge: 'dns-01', notBefore: iso(r.now - 20 * day), notAfter: iso(r.now + 70 * day), renewAt: iso(r.now + 40 * day), issuer: 'Let’s Encrypt' },
    names: { url: 'https://names.playkeeper.io' },
    termsAccepted: iso(claimed),
  }
}

const twoFactor: TwoFactorStatus = { state: 'off', recoveryCodesLeft: 0, appCodesBlocked: false }

const addonSources: AddonSources = { curseforge: { key: 'none' } }

// How it's running, and the memory each server needs.

function running(s: DemoState, r: Request): Running {
  const srv = serverOf(s, r)
  if (srv.phase !== 'online') return { status: 'unknown', title: '', explanation: '', evidence: [], causes: [], windowMinutes: 10 }
  const players = srv.players?.online ?? 0
  return { status: 'smooth', params: { tps: 20, mspt: 16 + players * 3 }, title: 'Running smoothly', explanation: 'It keeps up with the game, with time to spare every tick.', evidence: [], causes: [], windowMinutes: 10, at: iso(r.now), players }
}

const memoryBudgets = [2048, 3072, 4096, 6144, 8192]

function memoryAdvice(s: DemoState, r: Request): MemoryAdvice {
  const srv = serverOf(s, r)
  const budgetMB = srv.config?.memoryMB ?? 4096
  const live = s.machine.live
  const used = s.servers.reduce((n, x) => n + (x.config?.memoryMB ?? 0), 0)
  const room = (live?.memoryTotalMB ?? 24576) - (live?.systemReserveMB ?? 1536) - used + budgetMB
  const heap = (mb: number) => Math.round(mb * 0.75)
  // What each budget would be for the server, from the most it needed.
  const peakMB = srv.id === cobblemonId ? 4400 : srv.id === creativeId ? 1400 : 2560
  const fitOf = (mb: number): MemoryFit => (mb < peakMB ? 'too_tight' : mb < peakMB * 1.3 ? 'little_room' : mb < peakMB * 2 ? 'room_to_grow' : 'more_than_needed')
  const options = memoryBudgets.map((memoryMB) => ({ memoryMB, heapMB: heap(memoryMB), fits: memoryMB <= room, fit: fitOf(memoryMB) }))
  const days = Array.from({ length: 14 }, (_, i) => ({ date: iso(r.now - (13 - i) * day).slice(0, 10), peakMB: Math.round(peakMB * (0.8 + noise(i + budgetMB) * 0.2)) }))
  const base = { title: '', explanation: 'From how much memory it needed over the last 14 days.', evidence: [], actions: [], budgetMB, heapMB: heap(budgetMB), days, options }
  if (srv.id === cobblemonId) return { ...base, verdict: 'raise', params: { days: 9, to_mb: 6144 }, recommendedMB: 6144, days: days.slice(-9) }
  if (srv.id === creativeId) return { ...base, verdict: 'lower', params: { peak_mb: peakMB, days: 14, to_mb: 2048 }, recommendedMB: 2048 }
  return { ...base, verdict: 'keep', params: { peak_mb: peakMB, days: 14, reason: 'fits' }, recommendedMB: budgetMB }
}

// Picked by Playkeeper, and the pack friends need for a modded server.

function curated(s: DemoState, r: Request): CuratedAddons {
  const srv = serverOf(s, r)
  const mine = new Set(installedOn(s, srv.id).map(({ a }) => a))
  const order = ['rollback', 'pregenerate', 'newer-clients', 'essentials', 'permissions', 'lag-finder']
  const picks = libraryFor(srv).filter((a) => a.pick).sort((x, y) => order.indexOf(x.pick ?? '') - order.indexOf(y.pick ?? ''))
  return { picks: picks.map((a) => ({ id: a.pick ?? a.slug, card: card(a, r.now, mine.has(a)) })) }
}

const loaderVersion = (srv: ServerStatus) => srv.config?.software?.fabricLoader ?? srv.config?.software?.quiltLoader ?? srv.config?.software?.neoforgeVersion ?? ''

const needText: Record<ShareNeed, string> = { required: 'Friends need it', optional: 'Optional for friends', server_only: 'Server only', unknown: 'Unknown' }

function modsShare(s: DemoState, r: Request): PackShare {
  const srv = serverOf(s, r)
  if (!takesMods(srv)) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  const mods: ShareMod[] = installedOn(s, srv.id).map(({ a, inst }) => {
    const need = a.need ?? 'unknown'
    const v = versionOf(a, inst.version, inst.published)
    return { name: a.name, version: inst.version, path: `mods/${v.fileName ?? ''}`, from: 'user', source: a.source, project: a.projectId, page: card(a, r.now, true).pageUrl, onServer: true, need, label: { key: `share.need.${need}`, text: needText[need] }, inFile: need !== 'server_only' }
  })
  const needed = mods.filter((m) => m.need === 'required')
  const [first, second] = needed
  const notice =
    needed.length === 2 && first && second
      ? { key: 'share.notice.two', params: { mod: first.name, other: second.name }, text: `Friends need ${first.name} and ${second.name}` }
      : { key: 'share.notice.none', text: 'Friends can join without mods' }
  return {
    public: false,
    file: `${srv.slug}.mrpack`,
    size: 4_096,
    loaderName: typeName(srv.type),
    share: { server: srv.name, type: srv.type ?? 'fabric', minecraftVersion: srv.config?.minecraftVersion ?? '', loaderVersion: loaderVersion(srv), notice, mods },
  }
}

export const reads: Routes = {
  'GET /api/setup/status': () => ({ needsSetup: false }),
  'GET /api/auth/me': (_, r) => me(r.now),
  'GET /api/me/prefs': (s) => s.prefs,
  'GET /api/servers': (s) => s.servers,
  'GET /api/machines': (s) => [s.machine],
  'GET /api/machines/link': () => link,
  'GET /api/machines/:machine/activity': (s, r) => s.activity.slice(0, limit(r, 5)),
  'GET /api/machines/:machine/catalog': catalog,
  'GET /api/machines/:machine/catalog/builds': builds,
  'GET /api/machines/:machine/address': address,
  'GET /api/machines/:machine/addon-sources': () => addonSources,
  'GET /api/auth/2fa': () => twoFactor,
  'GET /api/machines/:machine/update': (_, r) => update(r.now),
  'GET /api/machines/:machine/preflight': preflight,
  'GET /api/machines/:machine/events': () => [],
  'GET /api/audit': (s) => s.audit,
  'GET /api/tokens': (s) => s.tokens,
  'GET /api/tokens/activity': (s) => s.agentActivity,
  'GET /api/servers/:id/logs': logs,
  'GET /api/servers/:id/activity': (s, r) => s.activity.filter((a) => a.serverId === serverOf(s, r).id).slice(0, limit(r, 5)),
  'GET /api/servers/:id/metrics': metrics,
  'GET /api/servers/:id/running': running,
  'GET /api/servers/:id/memory': memoryAdvice,
  'GET /api/servers/:id/players/sessions': sessions,
  'GET /api/servers/:id/players/summary': summary,
  'GET /api/servers/:id/whitelist': (s, r) => s.whitelist[serverOf(s, r).id] ?? [],
  'GET /api/servers/:id/operators': (s, r) => s.operators[serverOf(s, r).id] ?? [],
  'GET /api/servers/:id/backups': (s, r) => s.backups[serverOf(s, r).id] ?? [],
  'GET /api/servers/:id/addons': addons,
  'GET /api/servers/:id/addons/checks': addonChecks,
  'GET /api/servers/:id/addons/search': addonSearch,
  'GET /api/servers/:id/addons/curated': curated,
  'GET /api/servers/:id/mods/share': modsShare,
  'GET /api/servers/:id/addons/project/:source/:project': addonDetails,
  'GET /api/servers/:id/addons/project/:source/:project/removal': addonRemoval,
  'GET /api/servers/:id/pregen': pregen,
  'GET /api/servers/:id/resourcepack': resourcePack,
  'GET /api/servers/:id/datapacks': dataPacks,
}
