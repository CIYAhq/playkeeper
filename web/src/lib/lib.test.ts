import { describe, expect, it } from 'vitest'
import { templateQuery } from '@/api/templates'
import type { Address, Catalog, CatalogEntry, Crash, DNSRecord, FileRefusal, JoinAddress, LagCause, MachineEvent, MemoryAdvice, MemorySizing, MetricsBucket, Operation, Running, ServerConfig, ServerStatus, TemplateContents } from '@/api/types'
import { budgetAdvice, createRequest, freeName, styleMemory, versionCards, versionLine } from '@/components/app/create'
import { lineRuns } from '@/components/app/line-chart'
import { packRequest } from '@/pages/new-server'
import { passwordStrength } from '@/pages/onboarding'
import { certState, claimStep, dashboardURL, freeServers, freeStage, nameProblem, normalizeName, ownDone, recordFor, zoneOf } from './address'
import { niceMax, regroup, ticks } from './chart'
import { checklist, complete, progress } from './checklist'
import { behindSeconds, parseLine } from './console'
import { crashDetail, crashFixes, crashSummary, failureLine, lookupKey, phoneLines, preselect, refusalFixes, refusalLine } from './crash'
import { formatBytes, formatClock, formatCountdown, formatDate, formatDuration, formatList, formatMB, formatWhen, joinAddress, relativeAge, relativeTime, serverJoinAddress } from './format'
import { machineEventText } from './machines'
import { memoryAdviceLine, memoryOffers, memoryOptionHint, memoryProgress, memorySegments } from './memory'
import { busyReason, controls, createStepOf, isCreating, isSettingUp, packStepOf, phaseTone, statusLabel, statusTone, templateStepOf, whyNot } from './phase'
import { href, parse, type Route } from './router'
import { causeAction, causeText, cpuAxis, headlineTPS, memoryAxis, runningHeadline, tickRateAxis, tickTimeAxis, timeLabels } from './running'
import { newerStable, softwareLabel, softwareName } from './servers'
import { addonKind, formatReleased, shortHash } from './software'
import { memoryForStyle } from './styles'
import { addonsLine, afterSignIn, leftOutAddons, madeOn, packsLine, pinned, settingNames, settingsSummary, signInPath, templateFromHash } from './templates'
import { agentPhrase, elideSecret, mcpAddress, mcpSnippet, runsOutText, tokenExpired, tokenServersText } from './tokens'
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
      { name: 'server', slug: 'survival', tab: 'overview', page: 'running' },
      { name: 'machine', id: 'm2345abcde' },
      { name: 'machine-settings', id: 'm2345abcde' },
      { name: 'settings' },
      { name: 'ai-agents' },
      { name: 'account' },
      { name: 'account', section: 'two-factor' },
      { name: 'more' },
      { name: 'welcome' },
      { name: 'machines' },
      { name: 'machine-details', id: 'm2345abcde' },
      { name: 'new-server', machine: 'm2345abcde' },
    ]
    for (const r of routes) {
      const [path = '', query = ''] = href(r).split('?')
      expect(parse(path, query ? `?${query}` : '')).toEqual(r)
    }
    expect(parse('/settings/ai-agents/extra')).toEqual({ name: 'settings' })
    expect(parse('/settings/machines/Not-An-Id')).toEqual({ name: 'settings' })
    expect(parse('/servers/new', '?machine=../../x')).toEqual({ name: 'new-server' })
  })

  it('keeps 0.2.0 links working and sends unknown paths home', () => {
    expect(parse('/console')).toEqual({ name: 'legacy', tab: 'console' })
    expect(parse('/world/')).toEqual({ name: 'legacy', tab: 'world' })
    expect(parse('/servers/survival/nope')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/world/browse')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/overview/pregen')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/world/pregen/more')).toEqual({ name: 'home' })
    expect(parse('/servers/survival/running/more')).toEqual({ name: 'home' })
    expect(parse('/servers/Bad Slug')).toEqual({ name: 'home' })
    expect(parse('/account/nope')).toEqual({ name: 'account' })
    expect(parse('/machines/m2345abcde/settings/more')).toEqual({ name: 'home' })
    expect(parse('/machines/m2345abcde/nope')).toEqual({ name: 'home' })
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

  const sizing: MemorySizing = {
    workload: 'vanilla',
    budgets: [
      { memoryMB: 1536, heapMB: 1024, players: 0 },
      { memoryMB: 2048, heapMB: 1536, players: 4 },
      { memoryMB: 3072, heapMB: 2304, players: 4 },
      { memoryMB: 4096, heapMB: 3072, players: 10 },
      { memoryMB: 6144, heapMB: 4608, players: 20 },
    ],
    suggestions: [
      { players: 4, memoryMB: 2048 },
      { players: 10, memoryMB: 4096 },
      { players: 20, memoryMB: 6144 },
      { players: 40, memoryMB: 8192 },
    ],
  }
  const catalog = { memoryOptionsMB: [1536, 2048, 3072, 4096, 6144], maxMemoryMB: 6144, recommendedMemoryMB: 3072, sizing } as Catalog

  it("suggests what the sizing guide suggests for the style's players", () => {
    expect(styleMemory(catalog, 'friends')).toBe(4096)
    expect(styleMemory(catalog, 'creative')).toBe(4096)
    expect(styleMemory(catalog, 'solo')).toBe(2048)
    expect(budgetAdvice(catalog, 4096)).toEqual({ memoryMB: 4096, heapMB: 3072, players: 10 })
    expect(budgetAdvice(catalog, 1536)?.players).toBe(0)
  })

  it('offers the largest budget below the suggestion on a machine without room for it', () => {
    expect(styleMemory({ ...catalog, maxMemoryMB: 3072 }, 'friends')).toBe(3072)
  })

  it("uses the agent's own recommendation when the agent sends no sizing advice", () => {
    const older = { ...catalog, sizing: undefined }
    expect(styleMemory(older, 'friends')).toBe(3072)
    expect(budgetAdvice(older, 4096)).toBeUndefined()
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

  it('says how long ago in weeks and months for rare changes', () => {
    const now = Date.parse('2026-09-25T12:00:00Z')
    expect(relativeAge('2026-09-20T12:00:00Z', now)).toBe('5 days ago')
    expect(relativeAge('2026-09-04T12:00:00Z', now)).toBe('3 weeks ago')
    expect(relativeAge('2026-05-25T12:00:00Z', now)).toBe('4 months ago')
    expect(relativeAge('2023-09-25T12:00:00Z', now)).toBe('3 years ago')
  })

  it('counts down whole seconds, rounding up so it never says 0:00 early', () => {
    expect(formatCountdown(48)).toBe('0:48')
    expect(formatCountdown(0.2)).toBe('0:01')
    expect(formatCountdown(960)).toBe('16:00')
    expect(formatCountdown(3725)).toBe('1:02:05')
    expect(formatCountdown(-5)).toBe('0:00')
  })

  it('builds the join address players type', () => {
    expect(joinAddress('198.51.100.10', 25565)).toBe('198.51.100.10')
    expect(joinAddress('198.51.100.10', 25567)).toBe('198.51.100.10:25567')
    expect(joinAddress('2001:db8::1', 25566)).toBe('[2001:db8::1]:25566')
  })

  it('prefers the friendly join address once it works', () => {
    expect(serverJoinAddress({ gamePort: 25566 }, '198.51.100.10')).toBe('198.51.100.10:25566')
    expect(serverJoinAddress({ gamePort: 25566, joinAddress: 'creative.alex.playkeeper.io' }, '198.51.100.10')).toBe('creative.alex.playkeeper.io')
  })

  it('gives a recent row the time today, yesterday, or its date', () => {
    const now = new Date(2026, 8, 25, 20, 0)
    const today = new Date(2026, 8, 25, 18, 2).toISOString()
    const earlier = new Date(2026, 8, 12, 9, 30).toISOString()
    expect(formatWhen(today, now)).toBe(formatClock(today))
    expect(formatWhen(new Date(2026, 8, 24, 23, 59).toISOString(), now)).toBe('yesterday')
    expect(formatWhen(earlier, now)).toBe(formatDate(earlier))
  })
})

describe('AI agents', () => {
  it('connects agents to the dashboard’s name when it has one', () => {
    expect(mcpAddress([{ kind: 'ip', address: '203.0.113.10:8443' }, { kind: 'name', address: 'alex.playkeeper.io:8443' }], 'https://203.0.113.10:8443')).toBe('https://alex.playkeeper.io:8443/mcp')
    expect(mcpAddress([{ kind: 'ip', address: '203.0.113.10:8443' }], 'https://203.0.113.10:8443')).toBe('https://203.0.113.10:8443/mcp')
    expect(mcpAddress(undefined, 'https://localhost:8448')).toBe('https://localhost:8448/mcp')
  })

  it('writes MCP settings an AI tool can read, and shows the secret cut short', () => {
    const secret = 'pk_mcp_abcdefghijklmnopqrstuvwxyz'
    const snippet = mcpSnippet('https://alex.playkeeper.io:8443/mcp', secret)
    expect(JSON.parse(snippet)).toEqual({ mcpServers: { playkeeper: { url: 'https://alex.playkeeper.io:8443/mcp', headers: { Authorization: `Bearer ${secret}` } } } })
    expect(elideSecret(secret)).toBe('pk_mcp_abcd…')
    expect(elideSecret('pk_mcp_ab')).toBe('pk_mcp_ab')
  })

  it('names a token’s servers, counting ones since deleted', () => {
    const servers = [
      { id: 'a', name: 'Survival' },
      { id: 'b', name: 'Creative' },
    ]
    expect(tokenServersText({ allServers: true, servers: [] }, servers)).toBe('All servers')
    expect(tokenServersText({ allServers: false, servers: ['a'] }, servers)).toBe('Survival')
    expect(tokenServersText({ allServers: false, servers: ['a', 'b'] }, servers)).toBe('Survival and Creative')
    expect(tokenServersText({ allServers: false, servers: ['a', 'gone'] }, servers)).toBe('2 servers')
  })

  it('says when a token runs out', () => {
    const now = Date.parse('2026-09-25T12:00:00Z')
    expect(runsOutText('2026-11-22T13:00:00Z', now)).toBe('in 58 days')
    expect(runsOutText('2026-09-26T13:00:00Z', now)).toBe('in 1 day')
    expect(runsOutText('2026-09-25T20:00:00Z', now)).toBe('today')
    expect(runsOutText('2026-09-25T12:00:00Z', now)).toBe('Ran out')
    expect(tokenExpired({ expiresAt: '2026-09-25T12:00:00Z' }, now)).toBe(true)
    expect(tokenExpired({ expiresAt: '2026-09-25T12:00:01Z' }, now)).toBe(false)
  })

  it('says what an agent did in words, even with a tool it doesn’t know', () => {
    expect(agentPhrase({ tool: 'create_backup', serverName: 'Survival' })).toBe('made a backup of Survival')
    expect(agentPhrase({ tool: 'list_online_players', serverName: 'Survival' })).toBe('checked who’s online on Survival')
    expect(agentPhrase({ tool: 'install_addon', serverName: 'Survival' })).toBe('installed a plugin or mod on Survival')
    expect(agentPhrase({ tool: 'future_tool', serverName: 'Survival' })).toBe('used future_tool on Survival')
    expect(agentPhrase({ tool: 'future_tool' })).toBe('used future_tool')
    expect(agentPhrase({ tool: 'toString', serverName: 'Survival' })).toBe('used toString on Survival')
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

  it('says to reinstall first when a server’s software changed', () => {
    const change = { file: 'paper-26.1.2-74.jar', algorithm: 'sha256', recorded: 'a'.repeat(64), found: 'b'.repeat(64), detectedAt: '2026-09-25T10:00:00Z', software: 'Paper 26.1.2 build 74' }
    expect(whyNot(server({ phase: 'crashed', softwareChanged: change }), 'start', false)).toBe('Reinstall the server software first.')
  })

  it('calls a server creating only while its create runs', () => {
    const at = '2026-09-25T10:00:00Z'
    expect(isCreating(server({ operation: { id: '1', kind: 'create', status: 'running', phase: 'downloading_server', actor: 'a', startedAt: at } }))).toBe(true)
    expect(isCreating(server({ phase: 'stopped', lastOperation: { id: '1', kind: 'create', status: 'failed', phase: 'downloading_server', actor: 'a', startedAt: at } }))).toBe(false)
    expect(isCreating(server({ operation: { id: '2', kind: 'start', status: 'running', phase: 'starting', actor: 'a', startedAt: at }, lastOperation: { id: '1', kind: 'create', status: 'failed', phase: '', actor: 'a', startedAt: at } }))).toBe(false)
  })

  it('says in a few words why a control can’t be used', () => {
    const backup = { id: '1', kind: 'backup', status: 'running', phase: '', actor: 'a', startedAt: '2026-09-25T10:00:00Z' } as const
    expect(whyNot(server(), 'restart', false)).toBeUndefined()
    expect(whyNot(server(), 'start', false)).toBe('Survival is already running.')
    expect(whyNot(server({ phase: 'stopped' }), 'stop', false)).toBe('Survival is already stopped.')
    expect(whyNot(server({ phase: 'stopped' }), 'command', false)).toBe('Start Survival first.')
    expect(whyNot(server({ phase: 'stopping' }), 'start', false)).toBe('Stopping Survival. Try again when it’s done.')
    expect(whyNot(server({ phase: 'starting' }), 'command', false)).toBe('Starting Survival. Try again when it’s done.')
    expect(whyNot(server({ operation: backup }), 'change', false)).toBe('Backing up Survival. Try again when it’s done.')
    expect(whyNot(server({ exists: false, phase: 'not_created' }), 'start', false)).toBe('Survival isn’t set up yet.')
    expect(whyNot(server({ phase: 'docker_unavailable' }), 'change', false)).toBe('Docker not responding')
    expect(whyNot(server(), 'change', true)).toBe('Waiting for the Playkeeper agent to answer.')
    expect(whyNot(server(), 'restart', 'Can’t reach home-server')).toBe('Can’t reach home-server')
    expect(whyNot(server(), 'restart', undefined)).toBeUndefined()
    expect(busyReason(server())).toBeUndefined()
    expect(busyReason(server({ operation: backup }))).toBe('Backing up Survival. Try again when it’s done.')
  })

  it('maps phases to tones and setup steps', () => {
    expect(phaseTone('preparing_world')).toBe('busy')
    expect(phaseTone('not_created')).toBe('stopped')
    expect(createStepOf('verifying_download')).toBe(1)
    expect(createStepOf('preparing_world')).toBe(2)
  })

  it('puts a modpack’s files between the software and the first start', () => {
    expect(packStepOf('')).toBe(0)
    expect(packStepOf('preparing_modpack')).toBe(1)
    expect(packStepOf('verifying_download')).toBe(1)
    expect(packStepOf('installing_modpack')).toBe(2)
    expect(packStepOf('installing_addons')).toBe(2)
    expect(packStepOf('starting_container')).toBe(3)
    expect(packStepOf('online')).toBe(4)
  })

  it('puts a template’s add-ons between the software and the first start', () => {
    expect(templateStepOf('')).toBe(0)
    expect(templateStepOf('verifying_download')).toBe(1)
    expect(templateStepOf('installing_addons')).toBe(2)
    expect(templateStepOf('preparing_world')).toBe(3)
    expect(templateStepOf('online')).toBe(4)
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
    expect(crashDetail(port)).toBeUndefined()
    expect(crashDetail({ ...port, params: { port: 25565, holder: 'java', holder_pid: 48211 } })).toBe('It’s java, process 48211, not started by Playkeeper.')
    expect(crashDetail({ ...port, params: { port: 25565, holder: 'java' } })).toBeUndefined()
    expect(crashDetail({ ...port, params: { port: 25565, holder_container: 'old-minecraft' } })).toBe('It’s the Docker container old-minecraft, not started by Playkeeper.')
    expect(crashDetail({ ...port, kind: 'disk_full', params: { holder_container: 'old-minecraft' } })).toBeUndefined()
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
      ['Install Multiverse-Core', 'Checking the library…'],
      ['Remove Multiverse-Portals', 'remove-addon'],
    ])
    expect(preselect(crashFixes(dep, 'Survival', 'my-vps', false))?.title).toBe('Remove Multiverse-Portals')
    const core = { source: 'modrinth', projectId: 'mvcore00' } as const
    const found = crashFixes(dep, 'Survival', 'my-vps', false, new Date(), { 'install:Multiverse-Core': { state: 'ready', key: core, name: 'Multiverse-Core', version: '5.1.2', fingerprint: 'f'.repeat(32), madeFor: '26.1.2' } })
    expect(found[0]).toMatchObject({ title: 'Install Multiverse-Core 5.1.2', hint: 'The version it asks for', recommended: true, plan: { kind: 'install-addon', key: core, fingerprint: 'f'.repeat(32) }, button: 'Install and start Survival' })
    expect(preselect(found)?.title).toBe('Install Multiverse-Core 5.1.2')
    const missing = crashFixes(dep, 'Survival', 'my-vps', false, new Date(), { 'install:Multiverse-Core': { state: 'unavailable', reason: 'Not in the library' } })
    expect(missing[0]).toMatchObject({ title: 'Install Multiverse-Core', reason: 'Not in the library' })
    expect(missing[0]?.plan).toBeUndefined()
  })

  it('updates the add-on that failed through the library, or says why it can’t', () => {
    const plugin = crash({
      start: true,
      kind: 'addon_failed',
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.0.2.jar' },
      fixes: [
        { kind: 'update_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Update it', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Remove it' },
      ],
    })
    expect(lookupKey(plugin.fixes[0]!)).toBe('update:Multiverse-Portals-5.0.2.jar')
    expect(lookupKey(plugin.fixes[1]!)).toBeUndefined()
    const portals = { source: 'modrinth', projectId: 'mvportal' } as const
    const ready = crashFixes(plugin, 'Survival', 'my-vps', false, new Date(), { 'update:Multiverse-Portals-5.0.2.jar': { state: 'ready', key: portals, name: 'Multiverse-Portals', version: '5.1.0', fingerprint: 'a'.repeat(32), madeFor: '26.1.2' } })
    expect(ready.map((o) => [o.title, o.hint, o.plan?.kind, o.button])).toEqual([
      ['Update Multiverse-Portals to 5.1.0', 'Made for 26.1.2', 'update-addon', 'Update and start Survival'],
      ['Remove Multiverse-Portals', 'Survival starts without it. The file is kept.', 'remove-addon', 'Remove and start Survival'],
    ])
    expect(preselect(ready)?.plan).toEqual({ kind: 'update-addon', key: portals, fingerprint: 'a'.repeat(32) })
    const byHand = crashFixes(plugin, 'Survival', 'my-vps', false, new Date(), { 'update:Multiverse-Portals-5.0.2.jar': { state: 'unavailable', reason: 'Added by hand, so Playkeeper can’t update it' } })
    expect(byHand[0]).toMatchObject({ title: 'Update Multiverse-Portals', reason: 'Added by hand, so Playkeeper can’t update it' })
    expect(preselect(byHand)?.title).toBe('Remove Multiverse-Portals')
  })

  it('says where the memory would come from on a phone', () => {
    const oom = crash({ kind: 'heap_out_of_memory', params: { budget_mb: 4096 }, fixes: [{ kind: 'raise_memory', params: { from_mb: 4096, to_mb: 6144 }, title: 'More memory', recommended: true }] })
    const [more] = crashFixes(oom, 'Survival', 'my-vps', true)
    expect(more).toMatchObject({ title: 'Give Survival 6 GB', hint: 'my-vps has 3.5 GB free', plan: { kind: 'settings', body: { memoryMB: 6144 } } })
    expect(crashFixes({ ...oom, roomMB: 0 }, 'Survival', 'my-vps', false)[0]?.hint).toBeUndefined()
  })

  it('names a planted link or named pipe in one line, with one way on, wherever a refused start shows', () => {
    const message = 'plugins/bStats/config.yml is not a normal file.'
    const refused = (code: FileRefusal['code'], type?: string): FileRefusal => ({ code, params: { path: 'plugins/bStats/config.yml', type }, message, hint: 'Remove it.' })
    const start: Operation = { id: 'op1', kind: 'start', status: 'failed', phase: '', actor: 'admin', startedAt: '2026-09-25T18:52:00Z', error: `Paper's bStats usage statistics could not be switched off, so the server was not started. ${message}` }
    const lines = {
      link: 'Playkeeper won’t start Survival while plugins/bStats/config.yml is a link. Delete it, or replace it with what it points to.',
      pipe: 'Playkeeper won’t start Survival while plugins/bStats/config.yml isn’t a normal file. Delete it.',
    }
    for (const [r, line] of [
      [refused('link'), lines.link],
      [refused('special_file', 'named_pipe'), lines.pipe],
    ] as const) {
      const s = server({ phase: 'stopped', refusal: r })
      expect(refusalLine(r, 'Survival')).toBe(line)
      expect(failureLine(start, s, 'my-vps')).toBe(line)
      expect(refusalFixes(r, 'Survival')).toEqual([{ id: 'again', recommended: true, title: 'Start Survival again', hint: 'Once it’s deleted', plan: { kind: 'start' }, button: 'Start Survival' }])
      expect([statusTone(s), statusLabel(s)]).toEqual(['crashed', 'Couldn’t start'])
    }
    expect(refusalLine(refused('too_large'), 'Survival')).toBe(`${message} Remove it.`)
    expect(refusalFixes(refused('too_large'), 'Survival')[0]?.hint).toBeUndefined()
    const pulled = { ...start, error: 'The server software could not be downloaded.' }
    expect(failureLine(pulled, server({ phase: 'stopped', refusal: refused('link') }), 'my-vps')).toBe(pulled.error)
    expect(failureLine(start, server({ phase: 'stopped', crash: crash({ start: true, kind: 'eula' }) }), 'my-vps')).toBe('The Minecraft EULA hasn’t been accepted.')
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
    expect(cards.map((c) => c.note)).toEqual(['Paper build 41', 'plugins and worlds can break', '', 'same as Survival'])
    expect(older.map((v) => v.id)).toEqual(['old', 'paper-1.21.11'])
  })

  it('writes each version’s release date and a note for its type', () => {
    const released = entry('26.2.1', { recommended: true, paperBuild: 41, releasedAt: '2026-09-02T09:30:00Z' })
    const date = formatReleased('2026-09-02T09:30:00Z')
    expect(date).toMatch(/Sep/)
    expect(versionLine(released, 'Paper build 41')).toBe(`Released ${date} · Paper build 41`)
    expect(versionLine(released, '')).toBe(`Released ${date}`)
    expect(versionLine(entry('26.2'), 'same as Survival')).toBe('same as Survival')
    const fabric = (v: string, over: Partial<CatalogEntry> = {}) => entry(v, { id: `fabric-${v}`, software: { type: 'fabric', minecraftVersion: v, fabricLoader: '0.17.2' }, build: '0.17.2', paperBuild: 0, ...over })
    const { cards } = versionCards([fabric('26.2.1', { recommended: true }), fabric('26.3', { experimental: true }), fabric('26.2')], [], false, 'fabric')
    expect(cards.map((c) => c.note)).toEqual(['most Fabric mods support it', 'many mods aren’t ready yet', ''])
    const purpur = entry('26.2.1', { id: 'purpur-26.2.1', recommended: true, software: { type: 'purpur', minecraftVersion: '26.2.1', purpurBuild: 2430 }, build: '2430', paperBuild: 0 })
    expect(versionCards([purpur], [], false, 'purpur').cards[0]?.note).toBe('Purpur build 2430')
    expect(versionCards([purpur], [], true, 'purpur').cards[0]?.note).toBe('latest stable, recommended')
  })

  it('only offers a server versions of its own type', () => {
    const fabricCfg = { type: 'fabric', minecraftVersion: '26.2', paperBuild: 0, software: { type: 'fabric', minecraftVersion: '26.2', fabricLoader: '0.16.14' } } as ServerConfig
    const fabric = (v: string, loader: string, over: Partial<CatalogEntry> = {}) => entry(v, { id: `fabric-${v}`, software: { type: 'fabric', minecraftVersion: v, fabricLoader: loader }, build: loader, paperBuild: 0, ...over })
    const mixed = [entry('26.2.1', { recommended: true }), fabric('26.2.1', '0.17.2', { recommended: true }), fabric('26.2', '0.17.2'), fabric('26.1.2', '0.17.2')]
    expect(upgradeTargets(fabricCfg, mixed).map((v) => v.id)).toEqual(['fabric-26.2.1', 'fabric-26.2'])
    expect(newerStable(fabricCfg, mixed)?.id).toBe('fabric-26.2.1')
    expect(newerStable(fabricCfg, [entry('26.3', { recommended: true })])).toBeUndefined()
  })

  it('keeps offering older versions PaperMC no longer updates', () => {
    const live = [entry('26.3', { experimental: true, channel: 'ALPHA' }), entry('26.2', { recommended: true, paperBuild: 129 }), entry('26.1.2', { supported: false, paperBuild: 74 }), entry('1.21.11', { supported: false, paperBuild: 132 })]
    const { cards, older } = versionCards(live, [])
    expect(cards.map((c) => c.entry.id)).toEqual(['paper-26.2', 'paper-26.3', 'paper-26.1.2'])
    expect(older.map((v) => v.id)).toEqual(['paper-1.21.11'])
    const legacy = [server({ id: 'x', name: 'Legacy', config: { minecraftVersion: '1.21.11', paperBuild: 132 } as ServerConfig })]
    expect(versionCards(live, legacy).cards.at(-1)?.note).toContain('Legacy')
  })
})

describe('creating a server', () => {
  it('picks a free name', () => {
    expect(freeName('Survival', [server()])).toBe('Survival 2')
    expect(freeName('Creative', [server()])).toBe('Creative')
  })

  it('turns hardcore on as a play style with hard difficulty', () => {
    const req = createRequest({ type: 'paper', versionId: 'v', acceptExperimental: false, style: 'friends', hardcore: true, levelType: 'flat', memoryMB: 4096, name: ' Hard ', motd: '', eula: true, build: '' })
    expect(req).toMatchObject({ name: 'Hard', motd: 'Hard', playStyle: 'hardcore', gameplay: { hardcore: true, difficulty: 'hard', levelType: 'flat', pvp: false } })
    expect(req).not.toHaveProperty('build')
  })

  it('sends the chosen build only for types that have one', () => {
    const base = { versionId: 'v', acceptExperimental: false, style: 'friends' as const, hardcore: false, levelType: 'normal' as const, memoryMB: 2048, name: 'Mods', motd: '', eula: true }
    expect(createRequest({ ...base, type: 'fabric', build: '0.17.2' })).toMatchObject({ type: 'fabric', build: '0.17.2' })
    expect(createRequest({ ...base, type: 'fabric', build: '' })).not.toHaveProperty('build')
    expect(createRequest({ ...base, type: 'vanilla', build: '0.17.2' })).not.toHaveProperty('build')
  })

  it('lets a modpack decide the type, version and game settings', () => {
    const c = { type: 'paper', versionId: 'paper-26.2', acceptExperimental: true, style: 'friends' as const, hardcore: false, levelType: 'flat' as const, memoryMB: 4096, name: 'Cobblemon', motd: '', eula: true, build: '41' }
    const pack = { source: 'modrinth' as const, projectId: 'TPK00001', versionId: 'TPV00001', name: 'Cobblemon Modpack', type: 'fabric', minecraftVersion: '1.21.1', memoryMB: 6144 }
    expect(packRequest(c, pack)).toEqual({ name: 'Cobblemon', acceptEula: true, memoryMB: 4096, motd: 'Cobblemon', maxPlayers: 10, acceptExperimental: false, modpack: { source: 'modrinth', projectId: 'TPK00001', versionId: 'TPV00001' } })
  })

  it('names each type’s build the way people say it', () => {
    expect(softwareName('paper', 41)).toBe('Paper build 41')
    expect(softwareName('fabric', '0.17.2')).toBe('Fabric loader 0.17.2')
    expect(softwareName('neoforge', '26.2.1.7')).toBe('NeoForge version 26.2.1.7')
    expect(softwareName('vanilla', 0)).toBe('Vanilla')
    expect(addonKind('purpur')).toBe('plugins')
    expect(addonKind('quilt')).toBe('mods')
    expect(addonKind('vanilla')).toBe('none')
    expect(shortHash('9f3c1a0e22b4c6d87b2da17e')).toBe('9f3c 1a0e … 7b2d a17e')
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

describe('machine events', () => {
  it('say what happened, newest first', () => {
    const events: MachineEvent[] = [
      { at: '2026-09-25T17:04:00Z', kind: 'machine.connected' },
      { at: '2026-09-25T17:02:00Z', kind: 'machine.disconnected', code: 'link_dropped' },
      { at: '2026-09-24T09:00:00Z', kind: 'machine.dashboard_updated', code: '0.3.1' },
      { at: '2026-09-12T16:40:00Z', kind: 'machine.joined', actor: 'siya', address: '203.0.113.24' },
    ]
    expect(events.map((_, i) => machineEventText(events, i))).toEqual([
      'Reconnected',
      'Lost the connection for 2 minutes · the network dropped',
      'The dashboard updated to Playkeeper 0.3.1',
      'Joined with a code siya made · from 203.0.113.24',
    ])
  })
})

describe('templates', () => {
  const contents = (over: Partial<TemplateContents> = {}): TemplateContents => ({
    name: 'Survival with friends',
    type: 'paper',
    minecraftVersion: '26.1.2',
    settings: {},
    addons: [
      { source: 'modrinth', name: 'Chunky', versionNumber: '1.4.40' },
      { source: 'hangar', name: 'LuckPerms', versionNumber: 'v5.5.0' },
    ],
    resourcePacks: 0,
    dataPacks: 0,
    packs: [],
    ...over,
  })

  it('says on which day a template was made, and never who made it', () => {
    const now = new Date('2026-09-26T04:00:00Z')
    expect(madeOn(contents({ created: '2026-09-25' }), now)).toMatch(/^made (25 Sep|Sep 25)$/)
    expect(madeOn(contents({ created: '2025-12-31' }), now)).toMatch(/^made (31 Dec 2025|Dec 31, 2025)$/)
    expect(madeOn({ ...contents({ created: '2026-09-25' }), author: 'siya' } as TemplateContents, now)).toMatch(/^made (25 Sep|Sep 25)$/)
    expect(madeOn(contents(), now)).toBeUndefined()
    expect(madeOn(contents({ created: 'yesterday' }), now)).toBeUndefined()
  })

  it('names the settings a template carries, four at most', () => {
    expect(settingNames({ difficulty: 'normal', pvp: false, viewDistance: 10, motd: 'Hi' })).toBe('Difficulty, PvP, view distance, server list message')
    expect(settingNames({ difficulty: 'normal', pvp: false, viewDistance: 10, motd: 'Hi', maxPlayers: 10, hardcore: false })).toBe('Difficulty, PvP, view distance, server list message and 2 more')
    expect(settingNames({ motd: '' })).toBe('')
  })

  it('sums up the settings for the create page', () => {
    expect(settingsSummary({ difficulty: 'normal', pvp: false, viewDistance: 10, maxPlayers: 10 })).toBe('Normal difficulty · friends can’t hurt each other · view distance 10 · up to 10 players')
    expect(settingsSummary({ hardcore: true, pvp: true })).toBe('Hardcore · friends can hurt each other')
    expect(settingsSummary({})).toBe('')
  })

  it('describes add-ons and packs', () => {
    expect(addonsLine(contents())).toBe('2 plugins')
    expect(addonsLine(contents({ type: 'fabric', addons: [{ source: 'modrinth', name: 'Lithium' }] }))).toBe('1 mod')
    expect(addonsLine(contents({ type: 'fabric', modpack: { source: 'modrinth', name: 'Fabulously Optimized', versionNumber: '9.0.0' } }))).toBe('Fabulously Optimized and 2 more mods')
    expect(addonsLine(contents({ type: 'fabric', addons: [], modpack: { source: 'modrinth', name: 'Fabulously Optimized' } }))).toBe('Fabulously Optimized')
    expect(packsLine(contents({ resourcePacks: 1, dataPacks: 3, packs: ['Faithful 32x', 'a', 'b', 'c'] }))).toBe('Faithful 32x and 3 data packs')
    expect(packsLine(contents({ resourcePacks: 1, packs: ['Faithful 32x'] }))).toBe('Faithful 32x')
    expect(packsLine(contents({ dataPacks: 1, packs: ['Terralith'] }))).toBe('1 data pack')
  })

  it('knows when every add-on keeps its version', () => {
    expect(pinned(contents())).toBe(true)
    expect(pinned(contents({ addons: [{ source: 'modrinth', name: 'Chunky' }] }))).toBe(false)
    expect(pinned(contents({ modpack: { source: 'modrinth', name: 'Pack' } }))).toBe(false)
  })

  it('lists the add-ons an export left out', () => {
    const notices = [
      { kind: 'left_out_addon_upload', params: { name: 'MyPlugin' }, message: '' },
      { kind: 'left_out_modpack', params: { name: 'Pack' }, message: '' },
      { kind: 'left_out_icon', message: '' },
      { kind: 'left_out_addon_missing', message: '' },
    ]
    expect(leftOutAddons(notices)).toEqual(['MyPlugin', 'Pack'])
  })

  it('carries a shared template through signing in', () => {
    expect(templateFromHash('#template=eyJ2IjoxfQ')).toBe('eyJ2IjoxfQ')
    expect(templateFromHash('#template=')).toBeUndefined()
    expect(templateFromHash('#code=abc')).toBeUndefined()
    expect(signInPath({ hash: '#template=eyJ2IjoxfQ' })).toBe('/login#template=eyJ2IjoxfQ')
    expect(signInPath({ hash: '' })).toBe('/login')
    expect(afterSignIn({ hash: '#template=eyJ2IjoxfQ' })).toBe('/servers/new#template=eyJ2IjoxfQ')
    expect(afterSignIn({ hash: '#code=abc' })).toBe('/')
  })

  it('asks for the parts the dialog leaves out', () => {
    expect(templateQuery({ addons: true, settings: true, packs: true, latest: false })).toBe('')
    expect(templateQuery({ addons: false, settings: false, packs: false, latest: true })).toBe('?addons=off&settings=off&packs=off&versions=latest')
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
    const land = cause({ kind: 'chunk_generation', actions: [{ kind: 'pregenerate_world', title: '', recommended: true }] })
    expect(causeAction(land, ctx)).toMatchObject({ mode: 'link', label: 'Pre-generate the map', href: '/servers/survival/world/pregen' })
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

  it('counts friends the sizing guide’s way when the machine sends it', () => {
    const sizing = { workload: 'paper', budgets: [{ memoryMB: 3072, heapMB: 2304, players: 4 }], suggestions: [] }
    const option = { memoryMB: 3072, heapMB: 2304, fits: true }
    expect(memoryOptionHint(option, undefined, 'my-vps', sizing)).toBe('Up to 4 friends')
    expect(memoryOptionHint(option, undefined, 'my-vps')).toBe('Up to 6 friends')
    const early = advice({ verdict: 'not_enough_data', budgetMB: 3072, params: { days: 1, min_days: 3 } })
    expect(memoryAdviceLine(early, 'my-vps', sizing)).toBe('Suggests a size after 3 days of play. Until then, 3 GB suits up to 4 friends.')
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

describe('address', () => {
  const claimedAt = '2026-09-25T10:00:00Z'
  const now = Date.parse('2026-09-25T10:05:00Z')
  const join = (over: Partial<JoinAddress>): JoinAddress => ({ serverId: 's1', name: 'Survival', port: 25565, label: '', published: false, ...over })
  const op = (over: Partial<Operation>): Operation => ({ id: 'op1', kind: 'address.publish', status: 'running', phase: 'pointing', actor: 'siya', startedAt: claimedAt, ...over })
  const address = (over: Partial<Address> = {}): Address => ({
    kind: 'playkeeper',
    host: 'alex.playkeeper.io',
    ip: '198.51.100.10',
    panelPort: 8443,
    base: 'playkeeper.io',
    servers: [join({ label: 'survival', address: 'survival.alex.playkeeper.io', published: true })],
    free: { name: 'alex', state: 'active', dns: 'ok', claimedAt, refreshedAt: claimedAt, checkedAt: claimedAt, holdDays: 30 },
    names: { url: 'https://names.playkeeper.io' },
    ...over,
  })

  it('reads a typed name the way the agent does', () => {
    expect(normalizeName('  Alex.PlayKeeper.io. ', 'playkeeper.io')).toBe('alex')
    expect(normalizeName('alex-mc', 'playkeeper.io')).toBe('alex-mc')
    expect(normalizeName('alex.example.com', 'playkeeper.io')).toBe('alex.example.com')
  })

  it('holds names to the service rule: 3 to 32 letters, digits and single inner dashes', () => {
    expect(nameProblem('')).toBe('empty')
    expect(nameProblem('al')).toBe('short')
    expect(nameProblem('a'.repeat(33))).toBe('long')
    for (const bad of ['alex--mc', '-alex', 'alex-', 'alex_mc', 'Alex', 'alex.mc']) expect(nameProblem(bad), bad).toBe('characters')
    for (const good of ['abc', 'alex-mc', 'a1-b2-c3', 'a'.repeat(32)]) expect(nameProblem(good), good).toBeUndefined()
  })

  it('gives free addresses to the first five servers whose slug can be a label', () => {
    const servers = ['survival', 'bad_slug', 'creative', 'a', 'b', 'c', 'd'].map((slug) => ({ slug }))
    expect(freeServers(servers).map((s) => s.slug)).toEqual(['survival', 'creative', 'a', 'b', 'c'])
    expect(freeServers([{ slug: 'x'.repeat(33) }])).toEqual([])
  })

  it('keeps the dashboard port in its address unless it is 443', () => {
    expect(dashboardURL('alex.playkeeper.io', 8443)).toBe('https://alex.playkeeper.io:8443')
    expect(dashboardURL('play.example.com', 443)).toBe('https://play.example.com')
  })

  it('names the zone where records are managed', () => {
    expect(zoneOf('play.example.com')).toBe('example.com')
    expect(zoneOf('example.com')).toBe('example.com')
    expect(zoneOf('play.example.co.uk')).toBe('example.co.uk')
    expect(zoneOf('mc.abc.io')).toBe('abc.io')
    expect(zoneOf('a.b.example.org')).toBe('example.org')
  })

  it('tells a claim from a refresh, and publishing, lapsed and done apart', () => {
    expect(freeStage(address({ operation: op({ startedAt: '2026-09-25T10:00:02Z' }) }))).toBe('claiming')
    expect(freeStage(address({ operation: op({ phase: 'certificate', startedAt: '2026-09-25T10:00:02Z' }) }))).toBe('claiming')
    expect(freeStage(address({ operation: op({ phase: 'publishing', startedAt: '2026-09-25T10:00:02Z' }) }))).toBe('publishing')
    expect(freeStage(address({ operation: op({ startedAt: '2026-11-04T10:00:00Z' }) }))).toBe('publishing')
    expect(freeStage(address({ operation: op({ status: 'succeeded' }) }))).toBe('done')
    expect(freeStage(address())).toBe('done')
    expect(freeStage(address({ servers: [join({ address: 'survival.alex.playkeeper.io', published: false })] }))).toBe('publishing')
    expect(freeStage(address({ free: { ...address().free!, dns: 'pending' } }))).toBe('publishing')
    expect(freeStage(address({ free: { ...address().free!, state: 'lapsed' } }))).toBe('lapsed')
  })

  it('says which claim step runs', () => {
    expect(claimStep(op({ phase: 'pointing' }))).toBe(1)
    expect(claimStep(op({ phase: '' }))).toBe(1)
    expect(claimStep(op({ phase: 'certificate' }))).toBe(2)
    expect(claimStep(undefined)).toBe(1)
  })

  it('reads the certificate: getting it, a problem, active or none', () => {
    const valid = { names: ['alex.playkeeper.io'], challenge: 'dns-01', notAfter: '2026-12-24T10:00:00Z' }
    expect(certState(address({ operation: op({ kind: 'certificate.issue', phase: 'checking' }) }), now)).toBe('getting')
    expect(certState(address({ operation: op({ phase: 'certificate' }) }), now)).toBe('getting')
    expect(certState(address({ certificate: { ...valid, problem: { code: 'rate_limited', message: 'Too many.' } } }), now)).toBe('problem')
    expect(certState(address({ certificate: valid }), now)).toBe('active')
    expect(certState(address({ certificate: { ...valid, notAfter: '2026-09-01T00:00:00Z' } }), now)).toBe('none')
    expect(certState(address(), now)).toBe('none')
  })

  it('calls an own domain done once its records check out and it has a certificate', () => {
    const check = { at: claimedAt, name: { name: 'play.example.com', ok: true, code: 'name_ok', message: '' }, ready: true }
    const certificate = { names: ['play.example.com'], challenge: 'http-01', notAfter: '2026-12-24T10:00:00Z' }
    const own = address({ kind: 'own', host: 'play.example.com', free: undefined, check, certificate })
    expect(ownDone(own, now)).toBe(true)
    expect(ownDone({ ...own, check: { ...check, ready: false } }, now)).toBe(false)
    expect(ownDone({ ...own, certificate: undefined }, now)).toBe(false)
    expect(ownDone({ ...own, kind: 'playkeeper' }, now)).toBe(false)
  })

  it('says which servers each record serves', () => {
    const servers = [join({}), join({ serverId: 's2', name: 'Creative', port: 25566, label: 'creative' })]
    const a: DNSRecord = { type: 'A', name: 'play.example.com', value: '198.51.100.10', ttl: 300 }
    const srv: DNSRecord = { serverId: 's2', type: 'SRV', name: '_minecraft._tcp.creative.play.example.com', value: '0 5 25566 play.example.com', ttl: 300, srv: { service: 'minecraft', protocol: 'tcp', host: 'creative.play.example.com', priority: 0, weight: 5, port: 25566, target: 'play.example.com' } }
    expect(recordFor(a, servers)).toBe('Dashboard and Survival')
    expect(recordFor(a, servers.slice(1))).toBe('Dashboard')
    expect(recordFor(srv, servers)).toBe('Creative, on port 25566')
    expect(recordFor(srv, servers, true)).toBe('Creative')
  })
})
