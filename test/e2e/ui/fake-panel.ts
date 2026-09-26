import type { Page, Route } from '@playwright/test'

// Stand-ins for the panel's API, so a browser test can drive a page without an
// agent, Docker or a Minecraft server. The shapes follow web/src/api/types.ts;
// the console buffer follows internal/agent/console.go.

export interface LogLine {
  seq: number
  ts: string
  text: string
}

export interface LogsResponse {
  epoch: string
  lines: LogLine[]
  next: number
  truncated: boolean
}

/** The agent's console buffer: the newest `capacity` lines, read by epoch and sequence number. */
export class FakeConsole {
  private lines: LogLine[] = []
  private next = 1
  /** Log reads the page has made. */
  reads = 0
  /** Called before each read, like a server printing between two polls. */
  beforeRead: (() => void) | undefined

  constructor(
    readonly capacity = 2000,
    readonly epoch = 'k2m9xd4qhw',
  ) {}

  append(texts: string[], at = Date.now()) {
    texts.forEach((text, i) => {
      this.lines.push({ seq: this.next++, ts: new Date(at - (texts.length - 1 - i) * 3).toISOString(), text })
    })
    if (this.lines.length > this.capacity) this.lines = this.lines.slice(-this.capacity)
  }

  get lastSeq(): number {
    return this.next - 1
  }

  get last(): LogLine | undefined {
    return this.lines[this.lines.length - 1]
  }

  /** Lines after `after` (at most `limit`, newest last), as the agent's ring answers. */
  since(epoch: string, after: number, limit: number): LogsResponse {
    if (limit <= 0 || limit > this.capacity) limit = 500
    if (epoch !== this.epoch) after = 0
    let sel = this.lines.filter((l) => l.seq > after)
    const first = this.lines[0]
    let truncated = !!first && after > 0 && first.seq > after + 1
    if (sel.length > limit) {
      sel = sel.slice(-limit)
      truncated = true
    }
    return { epoch: this.epoch, lines: sel, next: this.next - 1, truncated }
  }
}

const players = ['mara_k', 'tobi2009', 'JunoFox', 'Steve_Builds', 'pixelpaws']
const chat = [
  'anyone want to go to the nether?',
  'yes! bring the flint',
  'brb dinner',
  'who took all the iron from the chest by spawn',
  'found a village past the river, coordinates in the book at base',
  'can someone help me finish the roof of the big barn before it gets dark, the creepers keep blowing holes in it and I am running out of oak planks',
  'lol',
  'gg',
]
const advancements = ['Stone Age', 'Getting an Upgrade', 'Acquire Hardware', 'We Need to Go Deeper', 'Diamonds!']
const deaths = ['fell from a high place', 'was slain by Zombie', 'tried to swim in lava', 'was blown up by Creeper', 'drowned']

/** A small deterministic random source, so every run feeds the same log. */
function random(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let x = a
    x = Math.imul(x ^ (x >>> 15), x | 1)
    x ^= x + Math.imul(x ^ (x >>> 7), x | 61)
    return ((x ^ (x >>> 14)) >>> 0) / 4294967296
  }
}

/**
 * Console output a busy Paper server prints: chat, joins, saves, a lag
 * warning now and then, long lines that wrap and a plugin stack trace.
 */
export function paperLines(count: number, seed = 1, at = new Date()): string[] {
  const rnd = random(seed)
  const pick = <T>(xs: T[]): T => xs[Math.floor(rnd() * xs.length)] as T
  const clock = at.toISOString().slice(11, 19)
  const info = (msg: string) => `[${clock} INFO]: ${msg}`
  const out: string[] = []
  while (out.length < count) {
    const r = rnd()
    const who = pick(players)
    if (r < 0.34) out.push(info(`<${who}> ${pick(chat)}`))
    else if (r < 0.44) out.push(info(`${who} ${rnd() < 0.5 ? 'joined' : 'left'} the game`))
    else if (r < 0.52) out.push(info(`${who} has made the advancement [${pick(advancements)}]`))
    else if (r < 0.58) out.push(info(`${who} ${pick(deaths)}`))
    else if (r < 0.66) out.push(info('Saving the game (this may take a moment!)'), info('Saved the game'))
    else if (r < 0.74) out.push(info(`[Chunky] Task running for minecraft:overworld. Processed: ${Math.floor(rnd() * 9000)} chunks (${(rnd() * 100).toFixed(2)}%), ETA: 0:04:12, Rate: 61.2 cps, Current: -24, 31`))
    else if (r < 0.8) out.push(`[${clock} WARN]: Can't keep up! Is the server overloaded? Running ${2000 + Math.floor(rnd() * 3000)}ms or ${40 + Math.floor(rnd() * 60)} ticks behind`)
    else if (r < 0.84)
      out.push(
        `[${clock} ERROR]: Could not pass event PlayerInteractEvent to ExamplePlugin v1.2.0`,
        'java.lang.NullPointerException: Cannot invoke "org.bukkit.Location.getWorld()" because "loc" is null',
        '\tat com.example.plugin.InteractListener.onInteract(InteractListener.java:42) ~[ExamplePlugin-1.2.0.jar:?]',
      )
    else if (r < 0.9) out.push(info(`${who} issued server command: /home base`))
    else out.push(info(`[Server] Backup starts in ${1 + Math.floor(rnd() * 9)} minutes, expect a little lag`))
  }
  return out.slice(0, count)
}

export const me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 'test-csrf-token',
  expiresAt: '2026-09-26T18:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: {
    projectId: 'p2345abcde',
    role: 'admin',
    servers: { all: true },
    twoFactor: true,
    can: ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view'],
  },
}

export const machine = {
  id: 'm2345abcde',
  projectId: 'p2345abcde',
  name: 'my-vps',
  kind: 'local',
  live: {
    hostname: 'my-vps',
    os: 'Ubuntu 24.04',
    arch: 'x86-64',
    cpus: 4,
    cpuPercent: 22,
    memoryTotalMB: 16384,
    systemReserveMB: 1536,
    serversMemoryMB: 4096,
    memoryFreeMB: 10752,
    diskFreeBytes: 41 * 2 ** 30,
    diskTotalBytes: 80 * 2 ** 30,
    docker: true,
    dockerVersion: '27.3.1',
    agentVersion: '0.3.0',
    defaultGamePort: 25565,
    offlineModeTest: false,
    servers: 1,
  },
}

export function server() {
  const now = new Date().toISOString()
  return {
    id: 'abcdefghjk',
    name: 'Survival',
    slug: 'survival',
    game: 'minecraft-java',
    type: 'paper',
    createdAt: '2026-09-20T10:00:00Z',
    machineId: machine.id,
    exists: true,
    desired: 'running',
    phase: 'online',
    reachable: true,
    reachableAt: now,
    startedAt: '2026-09-25T08:00:00Z',
    players: { online: 3, max: 10, names: ['mara_k', 'tobi2009', 'JunoFox'], source: 'query', at: now },
    config: {
      type: 'paper',
      versionId: 'paper-26.1.2-74',
      minecraftVersion: '26.1.2',
      paperBuild: 74,
      memoryMB: 4096,
      heapMB: 3072,
      levelName: 'world',
      motd: 'Survival with friends',
      maxPlayers: 10,
      whitelist: true,
      eulaAcceptedAt: '2026-09-20T10:00:00Z',
      eulaAcceptedBy: 'siya',
      createdAt: '2026-09-20T10:00:00Z',
      image: 'itzg/minecraft-server:2026.9.0-java25',
      playStyle: 'friends',
    },
    gameplay: { difficulty: 'normal', pvp: true, gameMode: 'survival' },
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    resources: { cpuPercent: 31, tps: 19.8, memBytes: 2.1 * 2 ** 30, memLimitBytes: 4 * 2 ** 30, at: now },
    pendingRestart: false,
    firstSteps: { invited: 'mara_k', friendJoined: 'mara_k', friendJoinedAt: '2026-09-21T19:00:00Z', backedUp: true, downloaded: true },
  }
}

const update = { current: '0.3.0', supported: true, available: false, latest: '0.3.0', checkedAt: '2026-09-25T12:00:00Z' }

const pregen = { state: 'idle', world: 'world', chunks: 0, total: 0, percent: 0, etaSeconds: -1, pauseForPlayers: true, installed: false, presets: [] }
const offsite = { enabled: false, configured: false, type: '', place: '', copies: 0, copiesBytes: 0, queued: 0, providers: [] }

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
}

/**
 * Answers every /api call the page makes: a signed-in owner, one machine, one
 * online server, its console, and the World tab's pre-generation and pack rows
 * with nothing set up. Calls nothing here answers get a 404 and are
 * listed in `unexpected`, so a page that starts calling something new fails
 * its test instead of quietly showing an error.
 */
export async function fakePanel(page: Page, log: FakeConsole) {
  const s = server()
  const unexpected: string[] = []
  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const key = `${req.method()} ${url.pathname}`
    switch (key) {
      case 'GET /api/setup/status':
        return json(route, { needsSetup: false, machine: 'my-vps', version: '0.3.0' })
      case 'GET /api/auth/me':
        return json(route, me)
      case 'GET /api/servers':
        return json(route, [{ ...s, reachableAt: new Date().toISOString() }])
      case 'GET /api/machines':
        return json(route, [machine])
      case 'GET /api/me/prefs':
        return json(route, {})
      case `GET /api/machines/${machine.id}/update`:
        return json(route, update)
      case `GET /api/servers/${s.id}/logs`: {
        log.reads++
        log.beforeRead?.()
        const q = url.searchParams
        return json(route, log.since(q.get('epoch') ?? '', Number(q.get('after') ?? 0), Number(q.get('limit') ?? 0)))
      }
      case `GET /api/servers/${s.id}/pregen`:
        return json(route, pregen)
      case `GET /api/servers/${s.id}/resourcepack`:
        return json(route, { pending: false })
      case `GET /api/servers/${s.id}/datapacks`:
        return json(route, { packs: [], live: true })
      case `GET /api/servers/${s.id}/offsite`:
        return json(route, offsite)
      case `POST /api/servers/${s.id}/command`: {
        const { command } = req.postDataJSON() as { command: string }
        log.append([`[${new Date().toISOString().slice(11, 19)} INFO]: ${me.user.username} issued server command: /${command.replace(/^\//, '')}`])
        return json(route, { output: command.startsWith('list') ? 'There are 3 of a max of 10 players online: mara_k, tobi2009, JunoFox' : '' })
      }
      default:
        unexpected.push(key)
        return json(route, { error: 'Not found.', code: 'not_found' }, 404)
    }
  })
  return { server: s, unexpected }
}
