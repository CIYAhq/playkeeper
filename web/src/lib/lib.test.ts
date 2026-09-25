import { describe, expect, it } from 'vitest'
import type { CatalogEntry, Crash, LagCause, MemoryAdvice, MetricsBucket, Running, ServerConfig, ServerStatus } from '@/api/types'
import { createRequest, freeName, heapMB, versionCards } from '@/components/app/create'
import { lineRuns } from '@/components/app/line-chart'
import { passwordStrength } from '@/pages/onboarding'
import { niceMax, regroup, ticks } from './chart'
import { checklist, complete, progress } from './checklist'
import { behindSeconds, parseLine } from './console'
import { crashFixes, crashSummary, phoneLines, preselect } from './crash'
import { formatBytes, formatDuration, formatList, formatMB, joinAddress, relativeTime } from './format'
import { memoryAdviceLine, memoryOffers, memoryOptionHint, memoryProgress, memorySegments } from './memory'
import { controls, createStepOf, isSettingUp, phaseTone, statusLabel, statusTone } from './phase'
import { href, parse, type Route } from './router'
import { causeAction, causeText, cpuAxis, headlineTPS, memoryAxis, runningHeadline, tickRateAxis, tickTimeAxis, timeLabels } from './running'
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
      { name: 'server', slug: 'survival', tab: 'overview', page: 'running' },
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
    expect(parse('/servers/survival/running/more')).toEqual({ name: 'home' })
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
  const b = (state: MetricsBucket['state'], players: number | null = null): MetricsBucket => ({ start: '2026-09-25T00:00:00Z', playersMax: players, cpuAvg: null, memAvg: null, tpsAvg: null, msptAvg: null, coverage: 1, state })

  it('shows the most players in each bar, and a stop only when the server never ran', () => {
    const bars = regroup([b('online', 2), b('online', 5), b('offline'), b('offline'), b('offline'), b('no_data')], 3)
    expect(bars.map((x) => [x.state, x.players])).toEqual([
      ['online', 5],
      ['offline', null],
    ])
  })

  it('lines bars up with the clock', () => {
    const at = (minute: number, players: number): MetricsBucket => ({ start: new Date(Date.UTC(2026, 8, 25, 12, minute)).toISOString(), playersMax: players, cpuAvg: null, memAvg: null, tpsAvg: null, msptAvg: null, coverage: 1, state: 'online' })
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

  it('reads how far behind a lagging server is', () => {
    expect(behindSeconds('Running 2143ms or 42 ticks behind')).toBeCloseTo(2.143)
  })
})

describe('crash helper', () => {
  const crash = (over: Partial<Crash>): Crash => ({ at: '2026-09-25T18:53:00Z', start: false, kind: 'unknown', certain: true, title: '', explanation: 'The agent’s words.', evidence: [], fixes: [], lines: [], roomMB: 3584, ...over })
  const titles = (c: Crash, phone = false) => crashFixes(c, 'Survival', 'my-vps', phone).map((o) => [o.title, o.plan?.kind ?? o.reason])

  it('calls a stopped server whose start failed one that couldn’t start', () => {
    expect(statusTone(server({ phase: 'stopped', crash: crash({ start: true }) }))).toBe('crashed')
    expect(statusLabel(server({ phase: 'stopped', crash: crash({ start: true }) }))).toBe('Couldn’t start')
    expect(statusLabel(server({ phase: 'crashed' }))).toBe('Crashed')
    expect(statusLabel(server({ phase: 'stopped' }))).toBe('Stopped')
  })

  it('tells a port taken on the machine from one taken inside the server', () => {
    const port = crash({ kind: 'port_in_use', params: { port: 25565 }, fixes: [{ kind: 'change_port', params: { port: 25565 }, title: 'Change the port', recommended: true }, { kind: 'restart', title: 'Start again' }] })
    expect(crashSummary(port, 'Survival', 'my-vps')).toBe('Another program on my-vps is using port 25565.')
    expect(titles(port)).toEqual([
      ['Move Survival to another port', 'Coming later'],
      ['Start again on 25565', 'start'],
    ])
    expect(preselect(crashFixes(port, 'Survival', 'my-vps', false))?.title).toBe('Start again on 25565')
    expect(crashSummary(crash({ kind: 'port_in_use', params: { port: 25565, reason: 'in_use' } }), 'Survival', 'my-vps')).toBe('Something inside Survival was already using its port.')
    expect(crashSummary(crash({ kind: 'port_in_use' }), 'Survival', 'my-vps')).toBe('The agent’s words.')
  })

  it('names the missing add-ons and removes the one that needs them', () => {
    const dep = crash({
      kind: 'missing_dependency',
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.0.2.jar', dependencies: ['Multiverse-Core', 'Vault'] },
      fixes: [
        { kind: 'install_addon', params: { name: 'Multiverse-Core' }, title: 'Install Multiverse-Core', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Remove it' },
      ],
    })
    expect(crashSummary(dep, 'Survival', 'my-vps')).toBe('Multiverse-Portals needs Multiverse-Core and Vault, which aren’t installed.')
    expect(titles(dep)).toEqual([
      ['Install Multiverse-Core', 'Coming later'],
      ['Remove Multiverse-Portals', 'remove-addon'],
    ])
  })

  it('says where the memory would come from on a phone', () => {
    const oom = crash({ kind: 'heap_out_of_memory', params: { budget_mb: 4096 }, fixes: [{ kind: 'raise_memory', params: { from_mb: 4096, to_mb: 6144 }, title: 'More memory', recommended: true }] })
    const [more] = crashFixes(oom, 'Survival', 'my-vps', true)
    expect(more).toMatchObject({ title: 'Give Survival 6 GB', hint: 'my-vps has 3.5 GB free', plan: { kind: 'settings', body: { memoryMB: 6144 } } })
    expect(crashFixes({ ...oom, roomMB: 0 }, 'Survival', 'my-vps', false)[0]?.hint).toBeUndefined()
  })

  it('names a file Playkeeper refused and says to delete it', () => {
    const refused = (reason: string) => crash({ start: true, kind: 'refused_file', params: { path: 'plugins/bStats/config.yml', reason }, fixes: [{ kind: 'restart', title: 'Start the server again', recommended: true }] })
    expect(crashSummary(refused('link'), 'Survival', 'my-vps')).toBe('plugins/bStats/config.yml is a link, which Playkeeper won’t follow. Delete it, then start again.')
    expect(crashSummary(refused('special_file'), 'Survival', 'my-vps')).toBe('plugins/bStats/config.yml isn’t a normal file, so Playkeeper won’t open it. Delete it, then start again.')
    expect(crashSummary(refused('too_large'), 'Survival', 'my-vps')).toBe('The agent’s words.')
    expect(crashFixes(refused('link'), 'Survival', 'my-vps', false)).toMatchObject([{ title: 'Start Survival again', hint: 'Once it’s deleted', plan: { kind: 'start' }, button: 'Start Survival' }])
    expect(statusLabel(server({ phase: 'stopped', crash: refused('link') }))).toBe('Couldn’t start')
  })

  it('always leaves a way to start again', () => {
    const eula = crash({ kind: 'eula', fixes: [{ kind: 'accept_eula', title: 'Accept', recommended: true }] })
    expect(titles(eula)).toEqual([
      ['Accept the Minecraft EULA', 'Coming later'],
      ['Start Survival again', 'start'],
    ])
    expect(titles(crash({}))).toEqual([['Start Survival again', 'start']])
    expect(crashFixes(crash({}), 'Survival', 'my-vps', false)[0]?.recommended).toBe(true)
  })

  it('shows two short lines on a phone', () => {
    const lines = [{ text: 'Done (4.2s)!' }, { text: 'Stopping server' }, { text: 'java.lang.OutOfMemoryError: Java heap space' }]
    expect(phoneLines(lines).map((l) => l.text)).toEqual(['Stopping server', 'OutOfMemoryError: Java heap space'])
    expect(phoneLines([{ text: 'at net.minecraft.server.Main.main(Main.java:1)' }])[0]?.text).toBe('at net.minecraft.server.Main.main(Main.java:1)')
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

describe('how it’s running', () => {
  const ctx = { server: 'Survival', machine: 'my-vps', slug: 'survival', players: 3, minutes: 10 }
  const cause = (over: Partial<LagCause>): LagCause => ({ kind: 'world_workload', score: 40, title: 'Agent title', explanation: 'Agent words.', evidence: [], actions: [], ...over })
  const running = (over: Partial<Running>): Running => ({ status: 'smooth', title: '', explanation: '', evidence: [], causes: [], windowMinutes: 10, ...over })

  it('never says 20 of 20 while it is behind', () => {
    expect(headlineTPS(17.1, 20, true)).toBe(17)
    expect(headlineTPS(19.6, 20, true)).toBe(19.6)
    expect(headlineTPS(19.96, 20, false)).toBe(20)
  })

  it('says why it fell behind when there is no stretch to date it from', () => {
    expect(runningHeadline(running({ status: 'lagging', params: { overloads: 5 } }), 'Survival').title).toBe('Lagging')
    expect(runningHeadline(running({ status: 'lagging', params: { overloads: 5 } }), 'Survival').subtitle).toBe('It fell behind 5 times in the last 10 minutes.')
    expect(runningHeadline(running({ status: 'smooth', params: { tps: 20, mspt: 44 }, players: 0 }), 'Survival').subtitle).toBe('Nobody is on, but Survival is close to its limit.')
    expect(runningHeadline(running({ status: 'unknown', params: { sprinting: true } }), 'Survival').title).toBe('The game is sprinting')
  })

  it('offers the action Playkeeper can do now before one that is coming later', () => {
    const distances = cause({
      kind: 'high_distance',
      params: { simulation_distance: 14, view_distance: 16 },
      actions: [
        { kind: 'lower_simulation_distance', params: { from: 14, to: 10 }, title: '', recommended: true },
        { kind: 'lower_view_distance', params: { from: 16, to: 10 }, title: '' },
      ],
    })
    expect(causeAction(distances, ctx)).toMatchObject({ mode: 'link', label: 'Lower view distance to 10', href: '/servers/survival/settings?view=10#game' })
    expect(causeText(distances, ctx).body).toBe('View distance is 16 and simulation distance 14 chunks, a lot of land per player.')
    const profiler = cause({ actions: [{ kind: 'run_profiler', title: '', recommended: true }] })
    expect(causeAction(profiler, ctx)).toMatchObject({ mode: 'later', label: 'Run a profiler' })
    const host = cause({ kind: 'host_cpu_busy', params: { busy_percent: 97 }, actions: [{ kind: 'upgrade_host', params: { resource: 'cpu' }, title: '', recommended: true }] })
    expect(causeAction(host, ctx)).toMatchObject({ mode: 'advice', label: 'Move to a faster machine', note: 'At your hosting provider' })
    expect(causeText(host, ctx)).toEqual({ title: 'The processor is fully busy', body: 'Playkeeper can’t tell how much of that is Survival.', evidence: 'Processor 97% busy' })
  })

  it('keeps the agent’s words for a cause without the numbers its text needs', () => {
    expect(causeText(cause({ kind: 'cpu_steal' }), ctx)).toEqual({ title: 'Agent title', body: 'Agent words.', evidence: undefined })
    expect(causeText(cause({ kind: 'world_workload', params: { server_cpu: 96 } }), ctx).evidence).toBe('Used 1 core on average')
  })

  it('draws bad stretches in amber, joined to the line, and breaks it at gaps', () => {
    const bad = (v: number) => v < 19
    expect(lineRuns([20, 20, 17, 17, 20, 20], bad)).toEqual([
      { from: 0, values: [20, 20], bad: false },
      { from: 1, values: [20, 17, 17, 20], bad: true },
      { from: 4, values: [20, 20], bad: false },
    ])
    expect(lineRuns([20, null, 20, 20], bad)).toEqual([
      { from: 0, values: [20], bad: false },
      { from: 2, values: [20, 20], bad: false },
    ])
  })

  it('scales each chart from zero, with room above the tick budget', () => {
    expect(tickRateAxis(20)).toEqual({ max: 20, labels: ['20', '10', '0'] })
    expect(tickTimeAxis(50, [31, 58, null])).toEqual({ max: 80, labels: ['80', '40', '0'] })
    expect(tickTimeAxis(50, [240]).max).toBe(250)
    expect(memoryAxis(4096)).toEqual({ max: 4, labels: ['4 GB', '2', '0'] })
    expect(cpuAxis([22, 61])).toEqual({ max: 100, labels: ['100%', '50', '0'] })
  })

  it('labels the hour at quarters, ending with now', () => {
    const start = Date.parse('2026-09-25T17:52:00')
    const buckets: MetricsBucket[] = Array.from({ length: 61 }, (_, i) => ({ start: new Date(start + i * 60_000).toISOString(), playersMax: 3, cpuAvg: null, memAvg: null, tpsAvg: 20, msptAvg: 30, coverage: 1, state: 'online' }))
    expect(timeLabels(buckets, '1h').map((l) => l.text)).toEqual(['17:52', '18:07', '18:22', '18:37', 'now'])
    expect(timeLabels(buckets, '1h', true).map((l) => l.text)).toEqual(['17:52', '18:22', 'now'])
  })
})

describe('memory advice', () => {
  const advice = (over: Partial<MemoryAdvice>): MemoryAdvice => ({ verdict: 'keep', title: '', explanation: 'From the agent.', evidence: [], actions: [], budgetMB: 4096, heapMB: 3072, days: [], options: [], ...over })

  it('words each verdict as one line about the budget', () => {
    const line = (over: Partial<MemoryAdvice>) => memoryAdviceLine(advice(over), 'my-vps')
    expect(line({ params: { peak_mb: 2560, days: 14, reason: 'fits' } })).toBe('It never needed more than 2.5 GB in the last 14 days, so 4 GB is plenty.')
    expect(line({ params: { peak_mb: 2200, days: 1, reason: 'smallest' } })).toBe('It never needed more than 2.1 GB in the last day, so 4 GB is plenty.')
    expect(line({ params: { peak_mb: 2400, days: 9, reason: 'tight' } })).toBe('It needed up to 2.3 GB in the last 9 days, so 4 GB is just enough.')
    expect(line({ params: { peak_mb: 2400, days: 14, reason: 'ran_short_once' } })).toBe('It ran short of memory once in the last 14 days, so it shouldn’t have less than 4 GB.')
    expect(line({ params: { days: 14, reason: 'fits' } })).toBe('From the agent.')
    expect(line({ verdict: 'lower', params: { peak_mb: 1200, days: 14, to_mb: 3072 } })).toBe('It never needed more than 1.2 GB in the last 14 days, so 3 GB would be enough.')
    expect(line({ verdict: 'raise', params: { days: 14, to_mb: 6144 } })).toBe('It ran short of memory in the last 14 days, so give it 6 GB.')
    expect(line({ verdict: 'raise', params: { days: 3 } })).toBe('It ran short of memory in the last 3 days, and my-vps has none to spare.')
  })

  it('counts measured days, then a week, before suggesting a size', () => {
    const early = (params: Record<string, unknown>, over: Partial<MemoryAdvice> = {}) => advice({ verdict: 'not_enough_data', params: { min_days: 3, min_span_days: 7, ...params }, ...over })
    expect(memoryProgress(early({ days: 0 }))).toBeUndefined()
    expect(memoryProgress(early({ days: 1, span_days: 1 }))).toEqual({ day: 1, of: 3 })
    expect(memoryProgress(early({ days: 3, span_days: 5 }))).toEqual({ day: 5, of: 7 })
    expect(memoryProgress(advice({ params: { days: 14 } }))).toBeUndefined()
    expect(memoryAdviceLine(early({ days: 1 }), 'my-vps')).toBe('Suggests a size after 3 days of play. Until then, 4 GB suits up to 10 friends.')
    expect(memoryAdviceLine(early({ days: 4 }), 'my-vps')).toBe('Suggests a size after a week. Until then, 4 GB suits up to 10 friends.')
    expect(memoryAdviceLine(early({ days: 0 }, { fromNextStart: true, budgetMB: 2048 }), 'my-vps')).toBe('Starts measuring at its next restart. Until then, 2 GB suits up to 4 friends.')
  })

  it('says how each budget would fit, and which one it recommends', () => {
    const keep = advice({ recommendedMB: 4096 })
    const hint = (o: Partial<MemoryAdvice['options'][number]>, a: MemoryAdvice | undefined = keep) => memoryOptionHint({ memoryMB: 4096, heapMB: 3072, fits: true, ...o }, a, 'my-vps')
    expect(hint({ fit: 'room_to_grow' })).toBe('Recommended · room to grow')
    expect(hint({})).toBe('Recommended')
    expect(hint({ memoryMB: 2048, fit: 'too_tight' })).toBe('Too tight')
    expect(hint({ memoryMB: 3072, fit: 'little_room' })).toBe('Little room to spare')
    expect(hint({ memoryMB: 6144, fit: 'more_than_needed' })).toBe('More than it uses')
    expect(hint({ memoryMB: 8192, fit: 'more_than_needed', fits: false })).toBe('Not enough free on my-vps')
    expect(hint({ memoryMB: 6144 }, undefined)).toBe('Up to 20 friends')
  })

  it('always offers the budget the server has', () => {
    const catalog = { memoryOptionsMB: [2048, 4096, 8192], maxMemoryMB: 4096 }
    expect(memoryOffers(5120, undefined, catalog).map((o) => [o.memoryMB, o.fits])).toEqual([
      [2048, true],
      [4096, true],
      [5120, true],
      [8192, false],
    ])
    expect(memoryOffers(4096, advice({ options: [{ memoryMB: 6144, heapMB: 4608, fits: true }] }), catalog).map((o) => o.memoryMB)).toEqual([4096, 6144])
    expect(memoryOffers(4096, undefined, undefined).map((o) => o.memoryMB)).toEqual([4096])
  })
})
