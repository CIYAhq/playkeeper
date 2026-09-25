import { describe, expect, it } from 'vitest'
import type { CatalogEntry, MetricsBucket, ServerConfig, ServerStatus } from '@/api/types'
import { createRequest, freeName, heapMB, versionCards } from '@/components/app/create'
import { passwordStrength } from '@/pages/onboarding'
import { niceMax, regroup, ticks } from './chart'
import { checklist, complete, progress } from './checklist'
import { behindSeconds, parseLine, ranOutOfMemory } from './console'
import { formatBytes, formatDuration, formatList, formatMB, joinAddress, relativeTime } from './format'
import { memorySegments } from './memory'
import { controls, createStepOf, isSettingUp, phaseTone } from './phase'
import { href, parse, type Route } from './router'
import { newerStable, softwareLabel } from './servers'
import { memoryForStyle } from './styles'
import { upgradeTargets } from './versions'

function server(over: Partial<ServerStatus> = {}): ServerStatus {
  return {
    id: 'abcdefghjk',
    name: 'Survival',
    slug: 'survival',
    game: 'minecraft-java',
    type: 'paper',
    createdAt: '2026-09-20T10:00:00Z',
    exists: true,
    desired: 'running',
    phase: 'online',
    reachable: true,
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    gameplay: {},
    firstSteps: { backedUp: false, downloaded: false },
    ...over,
  }
}

function entry(v: string, over: Partial<CatalogEntry> = {}): CatalogEntry {
  return { id: `paper-${v}`, label: `Paper ${v}`, minecraftVersion: v, paperBuild: 10, jarSha256: '', java: 21, recommended: false, notes: '', channel: 'STABLE', experimental: false, supported: true, ...over }
}

describe('router', () => {
  it('reads and writes every page', () => {
    const routes: Route[] = [
      { name: 'home' },
      { name: 'new-server' },
      { name: 'server', slug: 'survival', tab: 'overview' },
      { name: 'server', slug: 'my-world-2', tab: 'players' },
      { name: 'server', slug: 'survival', tab: 'plugins' },
      { name: 'server', slug: 'survival', tab: 'plugins', sub: 'browse' },
      { name: 'server', slug: 'cobblemon', tab: 'mods', sub: 'browse' },
      { name: 'server', slug: 'survival', tab: 'world', sub: 'pregen' },
      { name: 'server', slug: 'survival', tab: 'world', sub: 'packs' },
      { name: 'machine', id: 'm2345abcde' },
      { name: 'settings' },
      { name: 'more' },
      { name: 'welcome' },
    ]
    for (const r of routes) expect(parse(href(r))).toEqual(r)
  })

  it('keeps 0.2.0 links working and sends unknown paths home', () => {
    expect(parse('/console')).toEqual({ name: 'legacy', tab: 'console' })
    expect(parse('/world/')).toEqual({ name: 'legacy', tab: 'world' })
    expect(parse('/servers/survival/nope')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/world/browse')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/overview/pregen')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/world/pregen/more')).toEqual({ name: 'home' })
    expect(parse('/servers/Bad Slug')).toEqual({ name: 'home' })
    expect(parse('/whatever')).toEqual({ name: 'home' })
  })
})

describe('first steps', () => {
  it('starts with creating a server when there is none', () => {
    const steps = checklist(undefined)
    expect(steps.map((s) => s.id)).toEqual(['create', 'invite', 'joined', 'backup', 'download'])
    expect(steps[0]?.state).toBe('next')
    expect(steps[4]?.state).toBe('locked')
  })

  it('never makes the step that ticks itself the next one', () => {
    const steps = checklist(server({ firstSteps: { invited: 'mara_k', backedUp: false, downloaded: false } }))
    expect(steps.map((s) => s.state)).toEqual(['done', 'todo', 'next', 'locked'])
    expect(progress(steps)).toMatchObject({ done: 1, total: 4, next: { id: 'backup' } })
  })

  it('unlocks the download after a backup and completes', () => {
    const fs = { invited: 'a', friendJoined: 'a', friendJoinedAt: '2026-09-24T10:00:00Z', backedUp: true, downloaded: false }
    expect(checklist(server({ firstSteps: fs })).map((s) => s.state)).toEqual(['done', 'done', 'done', 'next'])
    expect(complete(checklist(server({ firstSteps: { ...fs, downloaded: true } })))).toBe(true)
  })
})

describe('memory', () => {
  it('shares the machine without going over what it has', () => {
    const segs = memorySegments(8192, 1536, [{ id: 'a', name: 'Survival', memoryMB: 4096, running: true }], 4096)
    expect(segs.map((s) => [s.kind, s.memoryMB])).toEqual([
      ['system', 1536],
      ['running', 4096],
      ['new', 2560],
      ['free', 0],
    ])
  })

  it('suggests the largest size that fits a play style', () => {
    expect(memoryForStyle([1024, 2048, 3072, 4096, 6144], 4096)).toBe(4096)
    expect(memoryForStyle([1024, 2048, 3072], 4096)).toBe(3072)
    expect(memoryForStyle([6144, 8192], 4096)).toBe(6144)
    expect(memoryForStyle([], 4096)).toBe(0)
  })

  it('matches the agent on how much of it Java gets', () => {
    expect(heapMB(4096)).toBe(3072)
    expect(heapMB(1536)).toBe(1024)
  })
})

describe('formatting', () => {
  it('formats sizes, durations and lists', () => {
    expect(formatMB(4096)).toBe('4 GB')
    expect(formatMB(1536)).toBe('1.5 GB')
    expect(formatMB(768)).toBe('768 MB')
    expect(formatBytes(1.2 * 1024 ** 3)).toBe('1.2 GB')
    expect(formatBytes(undefined)).toBe('—')
    expect(formatDuration(12)).toBe('12 s')
    expect(formatDuration(200)).toBe('3 m 20 s')
    expect(formatDuration(48 * 60)).toBe('48 m')
    expect(formatDuration(2 * 3600 + 14 * 60)).toBe('2 h 14 m')
    expect(formatDuration(3 * 86400)).toBe('3 days')
    expect(formatList(['a', 'b', 'c'])).toBe('a, b and c')
  })

  it('says how long ago', () => {
    const now = Date.parse('2026-09-25T12:00:00Z')
    expect(relativeTime('2026-09-25T11:59:58Z', now)).toBe('just now')
    expect(relativeTime('2026-09-25T11:48:00Z', now)).toBe('12 min ago')
    expect(relativeTime('2026-09-24T11:00:00Z', now)).toBe('yesterday')
    expect(relativeTime(undefined, now)).toBe('never')
  })

  it('builds the join address players type', () => {
    expect(joinAddress('198.51.100.10', 25565)).toBe('198.51.100.10')
    expect(joinAddress('198.51.100.10', 25567)).toBe('198.51.100.10:25567')
    expect(joinAddress('2001:db8::1', 25566)).toBe('[2001:db8::1]:25566')
  })
})

describe('players chart', () => {
  const b = (state: MetricsBucket['state'], players: number | null = null): MetricsBucket => ({ start: '2026-09-25T00:00:00Z', playersMax: players, cpuAvg: null, memAvg: null, coverage: 1, state })

  it('shows the most players in each bar, and a stop only when the server never ran', () => {
    const bars = regroup([b('online', 2), b('online', 5), b('offline'), b('offline'), b('offline'), b('no_data')], 3)
    expect(bars.map((x) => [x.state, x.players])).toEqual([
      ['online', 5],
      ['offline', null],
    ])
  })

  it('lines bars up with the clock', () => {
    const at = (minute: number, players: number): MetricsBucket => ({ start: new Date(Date.UTC(2026, 8, 25, 12, minute)).toISOString(), playersMax: players, cpuAvg: null, memAvg: null, coverage: 1, state: 'online' })
    const bars = regroup([at(30, 1), at(40, 2), at(50, 3), at(60, 4), at(70, 5)], 6, 600)
    expect(bars.map((x) => [x.start.slice(11, 16), x.players])).toEqual([
      ['12:30', 3],
      ['13:00', 5],
    ])
  })

  it('keeps nobody online as a real zero', () => {
    expect(regroup([b('online', 0), b('offline')], 2)[0]).toMatchObject({ state: 'online', players: 0 })
  })

  it('picks readable axis steps', () => {
    expect(niceMax(0)).toBe(1)
    expect(niceMax(7)).toBe(10)
    expect(ticks(5)).toEqual([0, 3, 6])
    expect(ticks(10)).toEqual([0, 5, 10])
  })
})

describe('server state', () => {
  it('offers stop and restart when online, start when stopped', () => {
    expect(controls(server())).toMatchObject({ canStart: false, canStop: true, canRestart: true })
    expect(controls(server({ phase: 'stopped' }))).toMatchObject({ canStart: true, canStop: false, canRestart: false })
    expect(controls(server({ phase: 'docker_unavailable' }))).toMatchObject({ canStart: false, canStop: false })
  })

  it('knows a server that is still being set up', () => {
    const at = '2026-09-25T10:00:00Z'
    expect(isSettingUp(server({ operation: { id: '1', kind: 'create', status: 'running', phase: 'downloading_server', actor: 'a', startedAt: at } }))).toBe(true)
    expect(isSettingUp(server({ phase: 'stopped', lastOperation: { id: '1', kind: 'create', status: 'failed', phase: 'downloading_server', actor: 'a', startedAt: at } }))).toBe(true)
    expect(isSettingUp(server({ phase: 'online', startedAt: at, lastOperation: { id: '1', kind: 'create', status: 'succeeded', phase: '', actor: 'a', startedAt: at } }))).toBe(false)
  })

  it('maps phases to tones and setup steps', () => {
    expect(phaseTone('preparing_world')).toBe('busy')
    expect(phaseTone('not_created')).toBe('stopped')
    expect(createStepOf('verifying_download')).toBe(1)
    expect(createStepOf('preparing_world')).toBe(2)
  })
})

describe('console', () => {
  it('reads the time, level and kind of a line', () => {
    expect(parseLine('[18:31:02 WARN]: Can\u2019t keep up! Is the server overloaded? Running 2143ms or 42 ticks behind')).toMatchObject({ time: '18:31:02', level: 'WARN', kind: 'problem' })
    expect(parseLine('[18:31:40 INFO]: <mara_k> anyone want to go to the nether?')).toMatchObject({ kind: 'chat', text: '<mara_k> anyone want to go to the nether?' })
    expect(parseLine('[18:40:18 INFO]: JunoFox joined the game').kind).toBe('players')
    expect(parseLine('plain output').kind).toBe('info')
  })

  it('explains lag and out-of-memory crashes', () => {
    expect(behindSeconds('Running 2143ms or 42 ticks behind')).toBeCloseTo(2.143)
    expect(ranOutOfMemory(137, [])).toBe(true)
    expect(ranOutOfMemory(1, ['java.lang.OutOfMemoryError: Java heap space'])).toBe(true)
    expect(ranOutOfMemory(1, ['Stopping server'])).toBe(false)
  })
})

describe('versions', () => {
  const cfg = { minecraftVersion: '26.1.2', paperBuild: 74 } as ServerConfig
  const versions = [entry('26.2.1', { recommended: true, paperBuild: 41 }), entry('26.3', { experimental: true, channel: 'BETA' }), entry('26.2'), entry('26.1.2', { paperBuild: 80 }), entry('26.1.2', { id: 'old', paperBuild: 60 }), entry('1.21.11')]

  it('only offers versions a server can move to', () => {
    expect(upgradeTargets(cfg, versions).map((v) => `${v.minecraftVersion}/${v.paperBuild}`)).toEqual(['26.2.1/41', '26.3/10', '26.2/10', '26.1.2/80'])
    expect(newerStable(cfg, versions)?.minecraftVersion).toBe('26.2.1')
  })

  it('gives the latest stable, the newest experimental, the one before and matches to cards', () => {
    const others = [server({ id: 'x', name: 'Survival', config: { ...cfg } as ServerConfig })]
    const { cards, older } = versionCards(versions, others)
    expect(cards.map((c) => c.entry.id)).toEqual(['paper-26.2.1', 'paper-26.3', 'paper-26.2', 'paper-26.1.2'])
    expect(cards[3]?.hint).toContain('Survival')
    expect(older.map((v) => v.id)).toEqual(['old', 'paper-1.21.11'])
  })

  it('keeps offering older versions PaperMC no longer updates', () => {
    const live = [entry('26.3', { experimental: true, channel: 'ALPHA' }), entry('26.2', { recommended: true, paperBuild: 129 }), entry('26.1.2', { supported: false, paperBuild: 74 }), entry('1.21.11', { supported: false, paperBuild: 132 })]
    const { cards, older } = versionCards(live, [])
    expect(cards.map((c) => c.entry.id)).toEqual(['paper-26.2', 'paper-26.3', 'paper-26.1.2'])
    expect(older.map((v) => v.id)).toEqual(['paper-1.21.11'])
    const legacy = [server({ id: 'x', name: 'Legacy', config: { minecraftVersion: '1.21.11', paperBuild: 132 } as ServerConfig })]
    expect(versionCards(live, legacy).cards.at(-1)?.hint).toContain('Legacy')
  })
})

describe('creating a server', () => {
  it('picks a free name', () => {
    expect(freeName('Survival', [server()])).toBe('Survival 2')
    expect(freeName('Creative', [server()])).toBe('Creative')
  })

  it('turns hardcore on as a play style with hard difficulty', () => {
    const req = createRequest({ type: 'paper', versionId: 'v', acceptExperimental: false, style: 'friends', hardcore: true, levelType: 'flat', memoryMB: 4096, name: ' Hard ', motd: '', eula: true })
    expect(req).toMatchObject({ name: 'Hard', motd: 'Hard', playStyle: 'hardcore', gameplay: { hardcore: true, difficulty: 'hard', levelType: 'flat', pvp: false } })
  })

  it('rates passwords', () => {
    expect(passwordStrength('short')).toBe('short')
    expect(passwordStrength('abcdefghij')).toBe('weak')
    expect(passwordStrength('abcdefghijkl')).toBe('okay')
    expect(passwordStrength('correct horse battery')).toBe('strong')
  })

  it('labels the software', () => {
    expect(softwareLabel(server({ config: { minecraftVersion: '26.1.2' } as ServerConfig }))).toBe('Paper 26.1.2')
  })
})
