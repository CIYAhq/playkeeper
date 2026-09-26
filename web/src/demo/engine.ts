// The live demo's make-believe panel. It answers the dashboard's requests from
// DemoState, plays jobs out over a few seconds the way an agent reports them,
// and keeps players and the console moving. The state lives in
// sessionStorage: a reload keeps it, and it starts over on the hour and in
// every new tab.

import { ApiError } from '@/api/client'
import type { Activity, ActivityKind, ApiToken, Backup, Gameplay, NewToken, Operation, PlayStyle, PlayerStat, RestorePreview, ServerStatus, TokenRole } from '@/api/types'
import { t } from '@/i18n'
import { opLabel } from '@/lib/phase'
import { chatter, config, demoUser, demoVersion, fakeSha, fill, iso, logText, machineId, me, noise, reads, sample, sampleVersion, serverOf, update, versions, type DemoState, type Job, type JobKind, type Live, type Request, type Routes, type Step } from './data'
import { demoMarker } from './marker'
import { dt } from './messages'
import { demoToast, type DemoAction } from './toast'

const second = 1000
const minute = 60 * second
const hour = 60 * minute
const day = 24 * hour
const gb = 1024 ** 3
const keepLines = 500

let state: DemoState | undefined

function load(now: number): DemoState {
  state ??= saved()
  if (!state || state.sample !== sampleVersion || state.hour !== Math.floor(now / hour)) state = sample(now)
  return state
}

function saved(): DemoState | undefined {
  try {
    const text = sessionStorage.getItem(demoMarker)
    return text ? (JSON.parse(text) as DemoState) : undefined
  } catch {
    return undefined
  }
}

function save(s: DemoState) {
  try {
    sessionStorage.setItem(demoMarker, JSON.stringify(s))
  } catch {
    // Storage can be off or full; the demo then lasts as long as the page.
  }
}

/** Forgets this tab's demo, as a new visit would. */
export function resetDemo() {
  state = undefined
  try {
    sessionStorage.removeItem(demoMarker)
  } catch {
    // Nothing was saved.
  }
}

function say(s: DemoState, id: string, at: number, text: string) {
  const log = (s.logs[id] ??= [])
  log.push({ seq: (log.at(-1)?.seq ?? 0) + 1, ts: iso(at), text: logText(at, text) })
  if (log.length > keepLines) log.splice(0, log.length - keepLines)
}

function note(s: DemoState, at: number, serverId: string, kind: ActivityKind, more: Partial<Activity> = {}) {
  s.activity.unshift({ ts: iso(at), serverId, kind, ...more })
  s.activity.sort((a, b) => b.ts.localeCompare(a.ts))
  s.activity.splice(200)
}

function audit(s: DemoState, at: number, action: string, srv?: ServerStatus, target?: string, detail?: string) {
  s.audit.unshift({ id: (s.audit[0]?.id ?? 0) + 1, ts: iso(at), actor: demoUser, action, target: target ?? srv?.name, serverId: srv?.id, result: 'succeeded', detail, source: srv ? 'agent' : 'panel', machineId: srv ? machineId : undefined })
  s.audit.splice(200)
}

/** A player's row in the server's roster, made on their first visit. */
function seen(s: DemoState, serverId: string, name: string, at: number): PlayerStat {
  const roster = (s.roster[serverId] ??= [])
  let p = roster.find((x) => x.name === name)
  if (!p) {
    p = { name, lastSeen: iso(at), online: false, sessions: 0, playtimeSeconds: 0 }
    roster.push(p)
  }
  p.lastSeen = iso(at)
  return p
}

function leave(s: DemoState, srv: ServerStatus, live: Live, name: string, at: number, reason: string) {
  const p = live.online.find((x) => x.name === name)
  if (!p) return
  live.online = live.online.filter((x) => x !== p)
  say(s, srv.id, at, `${name} lost connection: ${reason}`)
  say(s, srv.id, at, `${name} left the game`)
  seen(s, srv.id, name, at).playtimeSeconds += Math.round((at - p.since) / second)
}

// How each job plays out, in the phases and console lines a real server goes
// through; the last step finishes it.
function plan(kind: JobKind, srv: ServerStatus, args: Record<string, string>): Step[] {
  const online = srv.phase === 'online'
  const target = versions.find((v) => v.id === args.versionId)
  const version = target?.minecraftVersion ?? srv.config?.minecraftVersion ?? ''
  const build = target?.paperBuild ?? srv.config?.paperBuild ?? 0
  const stopping: Step[] = [
    { at: 0, phase: 'stopping', op: 'stopping', lines: ['Stopping the server', 'Stopping server', 'Saving players'] },
    { at: 400, leave: true, lines: ['Saving worlds', "Saving chunks for level 'ServerLevel[world]'/minecraft:overworld", 'ThreadedAnvilChunkStorage: All dimensions are saved'] },
  ]
  const booting = (at: number): Step[] => [
    {
      at,
      phase: 'starting',
      op: 'starting',
      lines: [`Starting minecraft server version ${version}`, 'Loading properties', `This server is running Paper version ${version}-${build} (MC: ${version})`, `Default game type: ${(srv.gameplay.gameMode ?? 'survival').toUpperCase()}`, `Starting Minecraft server on *:${srv.gamePort}`],
    },
    { at: at + 2500, phase: 'preparing_world', op: 'preparing_world', lines: [`Preparing level "${srv.config?.levelName ?? 'world'}"`, 'Preparing start region for dimension minecraft:overworld'] },
    { at: at + 4500, phase: 'online', lines: ['Time elapsed: 1843 ms', `Done (${(4.2 + noise(at + srv.gamePort) * 2).toFixed(3)}s)! For help, type "help"`] },
  ]
  switch (kind) {
    case 'start':
      return [{ at: 0, phase: 'starting_container', op: 'starting_container' }, ...booting(1500)]
    case 'stop':
      return [...stopping, { at: 3000, phase: 'stopped' }]
    case 'restart':
      return [...stopping, ...booting(2500)]
    case 'backup':
      return [
        { at: 0, op: 'saving', lines: online ? ['Automatic saving is now disabled', 'Saving the game (this may take a moment!)', 'Saved the game'] : [] },
        { at: 1500, op: 'archiving' },
        { at: 3500, op: 'verifying' },
        { at: 5000, lines: online ? ['Automatic saving is now enabled'] : [] },
      ]
    case 'update-version':
      return online ? [...stopping, { at: 2000, phase: 'downloading_server', op: 'downloading_server' }, ...booting(4500)] : [{ at: 0, phase: 'downloading_server', op: 'downloading_server' }, { at: 3000, phase: 'stopped' }]
    case 'restore':
      return online ? [...stopping, { at: 2000, op: 'restoring' }, ...booting(4000)] : [{ at: 0, op: 'restoring' }, { at: 3000 }]
    case 'create':
      return [{ at: 0, phase: 'pulling_image', op: 'pulling_image' }, { at: 2500, phase: 'downloading_server', op: 'downloading_server' }, ...booting(5000)]
    default: {
      const unreachable: never = kind
      return unreachable
    }
  }
}

function busy(srv: ServerStatus): ApiError {
  return new ApiError(409, { error: dt('demo.busy', { what: srv.operation ? opLabel(srv.operation, srv.name) : srv.name }), code: 'busy' })
}

function begin(s: DemoState, srv: ServerStatus, kind: JobKind, now: number, args: Record<string, string> = {}): Operation {
  if (srv.operation) throw busy(srv)
  const steps = plan(kind, srv, args)
  const op: Operation = { id: `op${s.seq++}`, serverId: srv.id, kind, status: 'running', phase: steps[0]?.op ?? '', actor: demoUser, startedAt: iso(now) }
  srv.operation = op
  const job: Job = { kind, serverId: srv.id, start: now, steps, reached: 0, args }
  s.jobs[srv.id] = job
  play(s, job, now)
  return op
}

function play(s: DemoState, job: Job, now: number) {
  const srv = s.servers.find((x) => x.id === job.serverId)
  const live = s.live[job.serverId]
  if (!srv || !live) {
    delete s.jobs[job.serverId]
    return
  }
  for (let step = job.steps[job.reached]; step && job.start + step.at <= now; step = job.steps[job.reached]) {
    const at = job.start + step.at
    job.reached++
    if (step.leave) {
      for (const p of [...live.online]) {
        live.away.push(p.name)
        leave(s, srv, live, p.name, at, 'Server closed')
      }
      live.joins = []
    }
    step.lines?.forEach((text) => say(s, srv.id, at, text))
    if (step.phase) srv.phase = step.phase
    if (step.op && srv.operation) srv.operation.phase = step.op
    if (job.reached === job.steps.length) finish(s, srv, live, job, at)
  }
}

const toastFor: Record<JobKind, DemoAction> = { start: 'start', stop: 'stop', restart: 'restart', backup: 'backup', 'update-version': 'version', restore: 'restore', create: 'create' }

function finish(s: DemoState, srv: ServerStatus, live: Live, job: Job, at: number) {
  delete s.jobs[srv.id]
  const op = srv.operation
  srv.operation = undefined
  // A new id keeps the dashboard's own job toast quiet; the demo's shows instead.
  if (op) srv.lastOperation = { ...op, id: `${op.id}-demo`, status: 'succeeded', finishedAt: iso(at) }
  const actor = { actor: demoUser }
  switch (job.kind) {
    case 'start':
      up(srv, live, at)
      note(s, at, srv.id, 'started', actor)
      audit(s, at, 'start', srv)
      break
    case 'restart':
      up(srv, live, at)
      note(s, at, srv.id, 'restarted', actor)
      audit(s, at, 'restart', srv)
      break
    case 'create':
      up(srv, live, at)
      note(s, at, srv.id, 'created', { ...actor, detail: `Paper ${srv.config?.minecraftVersion ?? ''}` })
      audit(s, at, 'server.create', srv)
      break
    case 'stop':
      down(srv, at)
      note(s, at, srv.id, 'stopped', actor)
      audit(s, at, 'stop', srv)
      break
    case 'backup': {
      const b = backupOf(s, srv, at, 'manual', job.args.note)
      srv.lastBackup = b
      srv.firstSteps.backedUp = true
      if (srv.lastOperation) srv.lastOperation.detail = { downtimeMs: 0, backupId: b.id }
      note(s, at, srv.id, 'backup', actor)
      audit(s, at, 'backup.created', srv, b.fileName)
      break
    }
    case 'update-version': {
      const v = versions.find((x) => x.id === job.args.versionId)
      const from = srv.config?.minecraftVersion ?? ''
      if (v && srv.config) Object.assign(srv.config, { versionId: v.id, minecraftVersion: v.minecraftVersion, paperBuild: v.paperBuild, jarSha256: v.jarSha256, jarVerifiedAt: iso(at) })
      if (srv.phase === 'online') up(srv, live, at)
      note(s, at, srv.id, 'version', { ...actor, detail: `${from} → ${v?.minecraftVersion ?? from}` })
      audit(s, at, 'server.version', srv, undefined, `${from} → ${v?.minecraftVersion ?? from}`)
      break
    }
    case 'restore':
      backupOf(s, srv, at - 1500, 'rollback')
      if (srv.phase === 'online') up(srv, live, at)
      note(s, at, srv.id, 'restored', actor)
      audit(s, at, 'restore.applied', srv)
      break
    default: {
      const unreachable: never = job.kind
      return unreachable
    }
  }
  demoToast(toastFor[job.kind])
}

/** The server is online: players who left when it stopped come back one by one. */
function up(srv: ServerStatus, live: Live, at: number) {
  srv.phase = 'online'
  srv.desired = 'running'
  srv.reachable = true
  srv.reachableAt = iso(at)
  srv.startedAt = iso(at)
  srv.stoppedAt = undefined
  live.away.forEach((name, i) => live.joins.push({ name, at: at + 6 * second + i * 7 * second + Math.round(noise(at + i) * 4 * second) }))
  live.away = []
  live.nextLine = at + 8 * second
}

function down(srv: ServerStatus, at: number) {
  srv.phase = 'stopped'
  srv.desired = 'stopped'
  srv.reachable = false
  srv.stoppedAt = iso(at)
  srv.resources = undefined
}

function backupOf(s: DemoState, srv: ServerStatus, at: number, kind: Backup['kind'], note?: string): Backup {
  const list = (s.backups[srv.id] ??= [])
  const sizeBytes = Math.round((srv.worldBytes ?? 0.5 * gb) * (0.96 + noise(at) * 0.03))
  const b: Backup = {
    id: `bk${s.seq++}`,
    serverId: srv.id,
    kind,
    createdAt: iso(at),
    fileName: `${srv.slug}-${iso(at).slice(0, 16).replace(/[-:T]/g, '')}.tar.gz`,
    sizeBytes,
    sha256: fakeSha(at % 1_000_000),
    location: 'local',
    verified: true,
    verifiedAt: iso(at),
    downtimeMs: 0,
    minecraftVersion: srv.config?.minecraftVersion ?? '',
    levelName: srv.config?.levelName ?? 'world',
    fileCount: Math.round(sizeBytes / 150_000),
    createdBy: demoUser,
    note: note || undefined,
  }
  list.unshift(b)
  return b
}

// Everything that happens on its own between requests: jobs move on, players
// come back and chat, and the numbers wobble.
function tick(s: DemoState, now: number) {
  for (const job of Object.values(s.jobs)) play(s, job, now)
  for (const srv of s.servers) {
    const live = s.live[srv.id]
    if (!live || srv.phase !== 'online') {
      srv.players = undefined
      continue
    }
    for (const j of live.joins.filter((x) => x.at <= now)) {
      if (live.online.some((p) => p.name === j.name)) continue
      live.online.push({ name: j.name, since: j.at })
      say(s, srv.id, j.at, `${j.name} joined the game`)
      note(s, j.at, srv.id, 'joined', { player: j.name })
      seen(s, srv.id, j.name, j.at).sessions++
    }
    live.joins = live.joins.filter((x) => x.at > now)
    chat(s, srv, live, now)
    const names = live.online.map((p) => p.name)
    srv.players = { online: names.length, max: srv.config?.maxPlayers ?? 10, names, source: 'rcon list', at: iso(now) }
    srv.reachableAt = iso(now)
    const wobble = noise(Math.floor(now / (3 * second)) + srv.gamePort)
    srv.resources = {
      cpuPercent: Math.round(8 + names.length * 2 + wobble * 3),
      tps: 20,
      memBytes: (1.9 + names.length * 0.17 + wobble * 0.05) * gb,
      memLimitBytes: (srv.config?.memoryMB ?? 4096) * 1024 * 1024,
      diskFreeBytes: s.machine.live?.diskFreeBytes,
      diskTotalBytes: s.machine.live?.diskTotalBytes,
      at: iso(now),
    }
  }
  const m = s.machine.live
  if (m) {
    m.serversMemoryMB = s.servers.reduce((n, x) => n + (x.config?.memoryMB ?? 0), 0)
    m.memoryFreeMB = m.memoryTotalMB - m.systemReserveMB - m.serversMemoryMB
    m.servers = s.servers.length
    m.cpuPercent = Math.round(5 + s.servers.reduce((n, x) => n + (x.phase === 'online' ? (x.resources?.cpuPercent ?? 0) : 0), 0) * 1.2)
  }
}

/** A few console lines at a time, every few seconds, while anyone is on. */
function chat(s: DemoState, srv: ServerStatus, live: Live, now: number) {
  if (!live.online.length) {
    live.nextLine = Math.max(live.nextLine, now)
    return
  }
  for (let n = 0; n < 3 && live.nextLine <= now; n++) {
    say(s, srv.id, live.nextLine, fill(chatter[live.cursor % chatter.length] ?? '', live.online.map((p) => p.name), live.cursor))
    live.cursor++
    live.nextLine += 5 * second + Math.round(noise(live.cursor + srv.gamePort) * 7 * second)
  }
  if (live.nextLine <= now) live.nextLine = now + 5 * second
}

// The dashboard's writes, and what they change.
function playerName(r: Request): string {
  const name = String((r.body as { name?: string } | undefined)?.name ?? r.params.name ?? '').trim()
  if (!/^\w{3,16}$/.test(name)) throw new ApiError(400, { error: t('players.nameRule'), code: 'bad_name' })
  return name
}

function nameTaken(s: DemoState, name: string, except?: ServerStatus) {
  if (s.servers.some((x) => x !== except && x.name.toLowerCase() === name.toLowerCase())) throw new ApiError(409, { error: dt('demo.taken', { name }), code: 'name_taken' })
}

function newId(): string {
  const letters = 'abcdefghijkmnpqrstuvwxyz23456789'
  return Array.from({ length: 10 }, () => letters[Math.floor(Math.random() * letters.length)]).join('')
}

function slugFor(s: DemoState, name: string): string {
  const base = name.toLowerCase().normalize('NFKD').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'server'
  let slug = base
  for (let n = 2; s.servers.some((x) => x.slug === slug); n++) slug = `${base}-${n}`
  return slug
}

interface CreateBody {
  name?: string
  versionId?: string
  memoryMB?: number
  motd?: string
  maxPlayers?: number
  playStyle?: PlayStyle | ''
  gameplay?: Gameplay
}

function create(s: DemoState, r: Request): Operation {
  const b = (r.body ?? {}) as CreateBody
  const name = (b.name ?? '').trim()
  nameTaken(s, name)
  const v = versions.find((x) => x.id === b.versionId) ?? versions.find((x) => x.recommended) ?? versions[0]
  const memoryMB = b.memoryMB ?? 2048
  const m = s.machine.live
  if (m && memoryMB > m.memoryFreeMB) throw new ApiError(409, { error: dt('demo.noMemory', { machine: s.machine.name }), code: 'insufficient_memory' })
  let id = newId()
  while (s.servers.some((x) => x.id === id)) id = newId()
  const ports = new Set(s.servers.map((x) => x.gamePort))
  let port = 25565
  while (ports.has(port)) port++
  const created = iso(r.now)
  const srv: ServerStatus = {
    id,
    name,
    slug: slugFor(s, name),
    game: 'minecraft-java',
    type: 'paper',
    createdAt: created,
    machineId,
    exists: true,
    desired: 'running',
    phase: 'pulling_image',
    reachable: false,
    config: config({ versionId: v?.id, minecraftVersion: v?.minecraftVersion ?? '', paperBuild: v?.paperBuild, jarSha256: v?.jarSha256, memoryMB, motd: b.motd || name, createdAt: created, maxPlayers: b.maxPlayers ?? 10, playStyle: b.playStyle ?? '' }),
    gameplay: { difficulty: 'normal', pvp: true, gameMode: 'survival', hardcore: false, viewDistance: 10, levelType: 'normal', ...b.gameplay },
    gamePort: port,
    offlineModeTest: false,
    crashCount: 0,
    worldBytes: 0.02 * gb,
    pendingRestart: false,
    collectingSince: created,
    firstSteps: { backedUp: false, downloaded: false },
  }
  s.servers.push(srv)
  s.live[id] = { online: [], joins: [], away: [], nextLine: r.now, cursor: 0 }
  s.backups[id] = []
  s.logs[id] = []
  s.whitelist[id] = []
  s.operators[id] = []
  s.roster[id] = []
  return begin(s, srv, 'create', r.now)
}

interface SettingsBody {
  name?: string
  motd?: string
  maxPlayers?: number
  memoryMB?: number
  gameplay?: Gameplay
  restart?: boolean
}

function settings(s: DemoState, r: Request) {
  const srv = serverOf(s, r)
  if (srv.operation) throw busy(srv)
  const b = (r.body ?? {}) as SettingsBody
  if (b.name !== undefined) {
    nameTaken(s, b.name.trim(), srv)
    srv.name = b.name.trim()
  }
  if (srv.config) {
    if (b.motd !== undefined) srv.config.motd = b.motd
    if (b.maxPlayers !== undefined) srv.config.maxPlayers = b.maxPlayers
    if (b.memoryMB !== undefined) Object.assign(srv.config, { memoryMB: b.memoryMB, heapMB: Math.round(b.memoryMB * 0.75) })
  }
  if (b.gameplay) srv.gameplay = { ...srv.gameplay, ...b.gameplay }
  note(s, r.now, srv.id, 'settings', { actor: demoUser })
  audit(s, r.now, 'settings.changed', srv)
  return b.restart ? begin(s, srv, 'restart', r.now) : {}
}

function command(s: DemoState, r: Request) {
  const srv = serverOf(s, r)
  if (srv.phase !== 'online') throw new ApiError(409, { error: t('reason.startFirst', { server: srv.name }), code: 'not_running' })
  const text = String((r.body as { command?: string } | undefined)?.command ?? '').trim().replace(/^\//, '')
  const [verb = '', ...rest] = text.split(/\s+/)
  const names = s.live[srv.id]?.online.map((p) => p.name) ?? []
  const times: Record<string, number> = { day: 1000, noon: 6000, night: 13000, midnight: 18000 }
  const weather: Record<string, string> = { clear: 'clear', rain: 'rain', thunder: 'rain & thunder' }
  audit(s, r.now, 'console.command', srv, undefined, text)
  switch (verb.toLowerCase()) {
    case 'list':
      return { output: `There are ${names.length} of a max of ${srv.config?.maxPlayers ?? 10} players online: ${names.join(', ')}` }
    case 'say':
      say(s, srv.id, r.now, `[Server] ${rest.join(' ')}`)
      return { output: '' }
    case 'time': {
      const value = rest[0] === 'set' ? (times[rest[1] ?? ''] ?? Number(rest[1])) : NaN
      return { output: Number.isFinite(value) ? `Set the time to ${value}` : 'Unknown or incomplete command, see below for error' }
    }
    case 'weather': {
      const kind = weather[rest[0] ?? '']
      return { output: kind ? `Set the weather to ${kind}` : 'Unknown or incomplete command, see below for error' }
    }
    case 'tps':
      return { output: 'TPS from last 1m, 5m, 15m: 20.0, 20.0, 20.0' }
    case 'seed':
      return { output: 'Seed: [-4172144997902289642]' }
    case 'help':
      return { output: '/list, /say <message>, /time set <day|night>, /weather <clear|rain|thunder>, /tps, /seed' }
  }
  return { output: dt('demo.commands') }
}

function backupIn(s: DemoState, r: Request): [ServerStatus, Backup] {
  const srv = serverOf(s, r)
  const b = s.backups[srv.id]?.find((x) => x.id === r.params.backup)
  if (!b) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  return [srv, b]
}

function restorePreview(s: DemoState, r: Request): RestorePreview {
  const [srv, b] = backupIn(s, r)
  const level = srv.config?.levelName ?? 'world'
  const label = `Paper ${b.minecraftVersion}`
  return {
    id: `${srv.id}.${b.id}`,
    serverId: srv.id,
    source: b.fileName,
    receivedAt: iso(r.now),
    sizeBytes: b.sizeBytes,
    sha256: b.sha256,
    manifest: { createdAt: b.createdAt, playkeeperVersion: demoVersion, minecraftVersion: b.minecraftVersion, paperBuild: srv.config?.paperBuild ?? 0, versionId: `paper-${b.minecraftVersion}`, levelName: b.levelName, fileCount: b.fileCount, totalBytes: Math.round(b.sizeBytes * 2.4), sourceInstall: s.machine.name, settings: {} },
    compatible: true,
    problems: [],
    warnings: [],
    currentWorld: { exists: true, levelName: level, sizeBytes: srv.worldBytes ?? 0 },
    willCreateRollback: true,
    needsEula: false,
    memoryMB: srv.config?.memoryMB ?? 4096,
    confirmPhrase: `replace ${level}`,
    steps: [
      'Stop the running server (players are disconnected; downtime starts)',
      `Save a rollback archive of the current world "${level}"`,
      `Replace the current world with "${b.levelName}" from the backup`,
      `Start ${label} and wait until it is online`,
      'If the restored world fails to start, put the previous world back automatically',
    ],
    notRestored: [
      'Playkeeper sign-in accounts and sessions (this host keeps its own)',
      'Player analytics, sessions and the audit log from the source host',
      'Server jar and libraries (downloaded again from PaperMC after checksum verification)',
      'The RCON password (this host generates its own)',
    ],
  }
}

/** A download link was clicked: there is no file, so say so and count it as downloaded. */
function download(s: DemoState, r: Request) {
  const [srv, b] = backupIn(s, r)
  b.downloadedAt = iso(r.now)
  srv.firstSteps.downloaded = true
  if (srv.lastBackup?.id === b.id) srv.lastBackup = b
  note(s, r.now, srv.id, 'downloaded', { actor: demoUser })
  audit(s, r.now, 'backup.downloaded', srv, b.fileName)
  demoToast('download')
  return {}
}

/** Signing out of the demo only says there's nothing to sign out of: the answer never comes. */
const never = Symbol('never')

function signOut() {
  demoToast('signOut')
  return never
}

const writes: Routes = {
  'POST /api/setup': (_, r) => me(r.now),
  'POST /api/auth/login': (_, r) => me(r.now),
  'POST /api/auth/logout': signOut,
  'POST /api/auth/logout-all': signOut,
  'POST /api/auth/password': (s, r) => {
    audit(s, r.now, 'password.change', undefined, demoUser)
    return {}
  },
  'POST /api/me/prefs': (s, r) => Object.assign(s.prefs, r.body),
  'POST /api/servers/:id/start': (s, r) => begin(s, serverOf(s, r), 'start', r.now),
  'POST /api/servers/:id/stop': (s, r) => begin(s, serverOf(s, r), 'stop', r.now),
  'POST /api/servers/:id/restart': (s, r) => begin(s, serverOf(s, r), 'restart', r.now),
  'POST /api/servers/:id/backups': (s, r) => begin(s, serverOf(s, r), 'backup', r.now, { note: String((r.body as { note?: string } | undefined)?.note ?? '') }),
  'POST /api/servers/:id/backups/:backup/verify': (s, r) => {
    const [srv, b] = backupIn(s, r)
    Object.assign(b, { verified: true, verifiedAt: iso(r.now) })
    audit(s, r.now, 'backup.verified', srv, b.fileName)
    return b
  },
  'DELETE /api/servers/:id/backups/:backup': (s, r) => {
    const [srv, b] = backupIn(s, r)
    s.backups[srv.id] = (s.backups[srv.id] ?? []).filter((x) => x !== b)
    srv.lastBackup = s.backups[srv.id]?.[0]
    audit(s, r.now, 'backup.deleted', srv, b.fileName)
    return {}
  },
  'GET /api/servers/:id/backups/:backup/download': download,
  'POST /api/servers/:id/backups/:backup/restore': restorePreview,
  'DELETE /api/machines/:machine/restore/:preview': () => ({}),
  'POST /api/machines/:machine/restore/:preview/apply': (s, r) => {
    const srv = serverOf(s, { ...r, params: { id: r.params.preview?.split('.')[0] ?? '' } })
    return begin(s, srv, 'restore', r.now)
  },
  'POST /api/servers/:id/settings': settings,
  'POST /api/servers/:id/version': (s, r) => {
    const srv = serverOf(s, r)
    const v = versions.find((x) => x.id === (r.body as { versionId?: string } | undefined)?.versionId)
    if (!v) throw new ApiError(400, { error: dt('demo.noData'), code: 'bad_version' })
    return begin(s, srv, 'update-version', r.now, { versionId: v.id })
  },
  'POST /api/servers/:id/delete': (s, r) => {
    const srv = serverOf(s, r)
    if (srv.operation) throw busy(srv)
    s.servers = s.servers.filter((x) => x !== srv)
    s.activity = s.activity.filter((a) => a.serverId !== srv.id)
    for (const table of [s.live, s.backups, s.logs, s.whitelist, s.operators, s.roster, s.jobs, s.addons, s.pregen, s.packs] as Record<string, unknown>[]) delete table[srv.id]
    audit(s, r.now, 'server.deleted', srv)
    demoToast('delete')
    return {}
  },
  'POST /api/servers/:id/command': command,
  'POST /api/servers/:id/whitelist': (s, r) => {
    const srv = serverOf(s, r)
    const name = playerName(r)
    const list = (s.whitelist[srv.id] ??= [])
    if (!list.some((p) => p.name.toLowerCase() === name.toLowerCase())) list.push({ name })
    if (srv.phase === 'online') say(s, srv.id, r.now, `Added ${name} to the whitelist`)
    note(s, r.now, srv.id, 'allowlisted', { player: name, actor: demoUser })
    audit(s, r.now, 'whitelist.add', srv, name)
    return { message: `Added ${name} to the whitelist`, whitelist: list }
  },
  'DELETE /api/servers/:id/whitelist/:name': (s, r) => {
    const srv = serverOf(s, r)
    const name = playerName(r)
    s.whitelist[srv.id] = (s.whitelist[srv.id] ?? []).filter((p) => p.name.toLowerCase() !== name.toLowerCase())
    if (srv.phase === 'online') say(s, srv.id, r.now, `Removed ${name} from the whitelist`)
    note(s, r.now, srv.id, 'unlisted', { player: name, actor: demoUser })
    audit(s, r.now, 'whitelist.remove', srv, name)
    return { message: `Removed ${name} from the whitelist`, whitelist: s.whitelist[srv.id] }
  },
  'POST /api/servers/:id/operators': (s, r) => {
    const srv = serverOf(s, r)
    const name = playerName(r)
    const list = (s.operators[srv.id] ??= [])
    if (!list.some((p) => p.name === name)) list.push({ name, level: 4 })
    if (srv.phase === 'online') say(s, srv.id, r.now, `Made ${name} a server operator`)
    note(s, r.now, srv.id, 'operator', { player: name, actor: demoUser })
    audit(s, r.now, 'operator.add', srv, name)
    return { message: `Made ${name} a server operator`, operators: list }
  },
  'DELETE /api/servers/:id/operators/:name': (s, r) => {
    const srv = serverOf(s, r)
    const name = playerName(r)
    s.operators[srv.id] = (s.operators[srv.id] ?? []).filter((p) => p.name !== name)
    if (srv.phase === 'online') say(s, srv.id, r.now, `Made ${name} no longer a server operator`)
    note(s, r.now, srv.id, 'deoperator', { player: name, actor: demoUser })
    audit(s, r.now, 'operator.remove', srv, name)
    return { message: `Made ${name} no longer a server operator`, operators: s.operators[srv.id] }
  },
  'POST /api/servers/:id/kick': (s, r) => {
    const srv = serverOf(s, r)
    const name = playerName(r)
    const live = s.live[srv.id]
    if (live?.online.some((p) => p.name === name)) {
      leave(s, srv, live, name, r.now, 'Kicked by an operator')
      live.joins.push({ name, at: r.now + 40 * second })
    }
    note(s, r.now, srv.id, 'kicked', { player: name, actor: demoUser })
    audit(s, r.now, 'player.kick', srv, name)
    return { message: `Kicked ${name}` }
  },
  'POST /api/machines/:machine/servers': create,
  'POST /api/machines/:machine/update/check': (_, r) => update(r.now),
  'POST /api/machines/join-codes': () => {
    throw new ApiError(400, { error: dt('demo.noJoin'), code: 'demo' })
  },
  'DELETE /api/machines/join-codes/:code': () => ({}),
  'POST /api/tokens': (s, r): NewToken => {
    const b = (r.body ?? {}) as { name?: string; role?: TokenRole; allServers?: boolean; servers?: string[]; days?: number }
    const token: ApiToken = { id: `tk${s.seq++}`, name: (b.name ?? '').trim(), role: b.role ?? 'viewer', allServers: b.allServers ?? true, servers: b.servers ?? [], createdAt: iso(r.now), expiresAt: iso(r.now + (b.days ?? 60) * day), account: demoUser, mine: true }
    s.tokens.unshift(token)
    audit(s, r.now, 'token.create', undefined, token.name)
    return { token, secret: `pk_mcp_demo_${newId()}${newId()}` }
  },
  'DELETE /api/tokens/:token': (s, r) => {
    const gone = s.tokens.find((x) => x.id === r.params.token)
    s.tokens = s.tokens.filter((x) => x !== gone)
    if (gone) audit(s, r.now, 'token.revoke', undefined, gone.name)
    return {}
  },
}

const routes = Object.entries({ ...reads, ...writes }).map(([key, handler]) => {
  const [method = '', pattern = ''] = key.split(' ')
  const names: string[] = []
  const re = new RegExp(`^${pattern.replace(/:(\w+)/g, (_, name: string) => (names.push(name), '([^/]+)'))}$`)
  return { method, re, names, handler }
})

function route(method: string, path: string) {
  for (const r of routes) {
    const m = r.method === method ? r.re.exec(path) : null
    if (m) return { handler: r.handler, params: Object.fromEntries(r.names.map((n, i) => [n, decodeURIComponent(m[i + 1] ?? '')])) }
  }
  return undefined
}

/**
 * Answers one request the way the panel would, a moment later. A path the
 * demo has no data for fails like a missing endpoint, so its screen shows its
 * empty or error state rather than breaking.
 */
export async function answer(method: string, path: string, body?: unknown, raw?: Blob): Promise<unknown> {
  await new Promise((resolve) => setTimeout(resolve, 60 + Math.random() * 120))
  const now = Date.now()
  const s = load(now)
  try {
    tick(s, now)
    if (raw) throw new ApiError(400, { error: dt('demo.noUploads'), code: 'demo' })
    const url = new URL(path, 'http://demo.invalid')
    const found = route(method, url.pathname)
    if (!found) throw method === 'GET' ? new ApiError(404, { error: dt('demo.noData'), code: 'not_found' }) : new ApiError(400, { error: dt('demo.notHere'), code: 'demo' })
    const result = found.handler(s, { params: found.params, query: url.searchParams, body, now })
    if (result === never) return new Promise(() => undefined)
    return result === undefined ? undefined : structuredClone(result)
  } finally {
    save(s)
  }
}
