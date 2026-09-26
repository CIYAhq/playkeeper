// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Backup, MachineView, Me, Operation, PlayersSummary, Preflight, ServerConfig, ServerStatus } from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { activityText } from '@/components/app/activity'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { AiAgentsSection } from './ai-agents'
import { HomePage } from './home'
import { forgetJoinCode, MachinesSection } from './machines'
import { Onboarding } from './onboarding'
import { ServerPage } from './server'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { ServerSettingsPage } from './server/settings'
import { WorldPage } from './server/world'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  api: vi.fn(() => Promise.resolve({})),
}))

const me: Me = { user: { username: 'siya', role: 'owner' }, csrfToken: 't', expiresAt: '2026-09-26T00:00:00Z', idleTimeoutSeconds: 43200, version: '0.3.0' }

const machine: MachineView = {
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

const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 4096, heapMB: 3072, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true, playStyle: 'friends' } as ServerConfig

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
    reachableAt: new Date().toISOString(),
    startedAt: '2026-09-22T10:00:00Z',
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    gameplay: {},
    config,
    firstSteps: { backedUp: false, downloaded: false },
    ...over,
  }
}

function workspace(over: Partial<Workspace> = {}): Workspace {
  return {
    me,
    servers: [server()],
    serversError: undefined,
    machine,
    machines: [machine],
    prefs: {},
    setPrefs: async () => {},
    refresh: async () => {},
    updating: undefined,
    updatingSince: undefined,
    agentDown: false,
    stale: false,
    lastSeenAt: undefined,
    machineName: 'my-vps',
    lastSlug: undefined,
    setLastSlug: () => {},
    signOut: async () => {},
    ...over,
  }
}

function failed(kind: string, phase: string, error: string, detail?: Record<string, unknown>): Operation {
  const at = new Date(Date.now() - 60_000).toISOString()
  return { id: `${kind}-1`, kind, status: 'failed', phase, actor: 'siya', startedAt: at, finishedAt: at, error, detail }
}

/** Answers GETs by path prefix; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([prefix]) => path.includes(prefix))
    return hit ? Promise.resolve(hit[1]) : new Promise(() => {})
  }) as typeof client.get)
}

let root: Root | undefined

async function render(node: ReactNode, ws: Workspace = workspace()): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
  return document.body.textContent ?? ''
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
})

describe('Home', () => {
  it('starts empty with the five first steps', async () => {
    const text = await render(<HomePage />, workspace({ servers: [] }))
    expect(text).toContain('No servers yet')
    for (const step of ['Create your first server', 'Invite a friend', 'A friend joins', 'Make a backup', 'Download it']) expect(text).toContain(step)
  })

  it('shows each server with who is playing and its address', async () => {
    const text = await render(<HomePage />, workspace({ servers: [server({ players: { online: 3, max: 10, names: ['mara_k', 'tobi2009', 'JunoFox'], source: 'rcon list', at: '' } })] }))
    expect(text).toContain('Survival')
    expect(text).toContain('Paper 26.1.2')
    expect(text).toContain('3 playing')
    expect(text).toContain(window.location.hostname)
    expect(text).toContain('1 server on my-vps · 3 playing')
  })

  it('groups servers by machine, with activity from every machine that answers', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'home-server', memoryTotalMB: 32768 },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const away: MachineView = { ...home, id: 'a2345abcde', name: 'attic', live: undefined, link: { ...home.link!, machineId: 'a2345abcde', name: 'attic', state: 'offline' } }
    vi.mocked(client.get).mockImplementation(((path: string) => {
      if (path.includes(`/${machine.id}/activity`)) return Promise.resolve([{ ts: '2026-09-25T10:00:00Z', serverId: 'abcdefghjk', kind: 'backup', actor: 'siya' }])
      if (path.includes(`/${home.id}/activity`)) return Promise.resolve([{ ts: '2026-09-25T11:00:00Z', serverId: 'cobblemon1', kind: 'restarted', actor: 'siya' }])
      if (path.includes(`/${away.id}/`)) return Promise.reject(new client.ApiError(503, { error: 'offline', code: 'machine_offline' }))
      return new Promise(() => {})
    }) as typeof client.get)
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id })
    const attic = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: away.id, lastKnownAt: new Date().toISOString() })
    const text = await render(<HomePage />, workspace({ machines: [machine, home, away], servers: [server({ machineId: machine.id }), cobblemon, attic] }))
    expect(text).toContain('On my-vps')
    expect(text).toContain('On home-server')
    expect(text).toContain('On attic')
    expect(text).toContain('3 servers on 3 machines')
    const atticCard = [...document.querySelectorAll('article')].find((a) => a.textContent?.includes('Attic'))?.textContent ?? ''
    expect(atticCard).toContain('No live status')
    expect(atticCard).toContain('Can’t reach attic')
    const activity = [...document.querySelectorAll('li')].map((li) => li.textContent ?? '')
    const at = (line: string) => activity.findIndex((l) => l.includes(line))
    expect(at('Cobblemon restarted')).toBeGreaterThan(-1)
    expect(at('Cobblemon restarted')).toBeLessThan(at('You backed up Survival'))
    expect(vi.mocked(client.get).mock.calls.some(([p]) => String(p).includes(`/${away.id}/`))).toBe(false)
  })

  it('says when the agent stopped answering, keeping names but not numbers', async () => {
    const text = await render(<HomePage />, workspace({ agentDown: true, stale: true, servers: [server({ players: { online: 3, max: 10, names: [], source: '', at: '' } })] }))
    expect(text).toContain('Playkeeper can’t see your servers right now')
    expect(text).toContain('Survival')
    expect(text).toContain('No live status')
    expect(text).toContain('1 server on my-vps')
    expect(text).not.toContain('playing')
  })
})

describe('Activity', () => {
  it('names the AI agent or command-line user that acted, never its token id', () => {
    const token = { actor: 'token:t2345abcde', actorKind: 'token' as const, actorName: 'Claude on my laptop' }
    expect(activityText({ ts: '', kind: 'backup', ...token }, 'Survival', 'siya')).toBe('Claude on my laptop backed up Survival')
    expect(activityText({ ts: '', kind: 'restarted', ...token }, 'Survival', 'siya')).toBe('Claude on my laptop restarted Survival')
    expect(activityText({ ts: '', kind: 'stopped', actor: 'cli:alice', actorKind: 'cli', actorName: 'alice' }, 'Survival', 'siya')).toBe('alice stopped Survival')
    expect(activityText({ ts: '', kind: 'restarted', actor: 'siya' }, 'Survival', 'siya')).toBe('Survival restarted')
    expect(activityText({ ts: '', kind: 'backup', actor: 'siya' }, 'Survival', 'siya')).toBe('You backed up Survival')
  })
})

describe('Overview notices', () => {
  // Regression for item 66: a failure notice stayed up to 15 minutes after its
  // cause cleared, such as "Start failed" next to Online and Joinable.
  it('drops a failed start once the server is online', async () => {
    const last = failed('start', '', 'The server did not finish starting within 10m0s.')
    expect(await render(<Overview server={server({ phase: 'stopped', startedAt: undefined, lastOperation: last })} />)).toContain('Starting Survival failed')
    expect(await render(<Overview server={server({ phase: 'online', lastOperation: last })} />)).not.toContain('Starting Survival failed')
  })

  it('drops a backup refused for space once enough disk is free', async () => {
    const last = failed('backup', '', 'Not enough disk space for a backup.', { neededBytes: 2 ** 30 })
    const at = new Date().toISOString()
    expect(await render(<Overview server={server({ lastOperation: last, resources: { diskFreeBytes: 400 * 2 ** 20, at } })} />)).toContain('Backing up Survival failed')
    expect(await render(<Overview server={server({ lastOperation: last, resources: { diskFreeBytes: 17 * 2 ** 30, at } })} />)).not.toContain('Backing up Survival failed')
  })

  it('warns about low disk space with the preflight advice', async () => {
    const diskWarning = { id: 'disk', label: 'Disk space', status: 'fail' as const, detail: 'Only 0.4 GB free.', fix: 'Free at least 5 GB of disk space, then check again.' }
    const local = { ...machine, live: machine.live && { ...machine.live, diskWarning } }
    const text = await render(<Overview server={server()} />, workspace({ machine: local, machines: [local] }))
    expect(text).toContain('Low disk space: Only 0.4 GB free.')
    expect(text).toContain('Free at least 5 GB of disk space')
  })

  it('asks for a restart when settings changed', async () => {
    expect(await render(<Overview server={server({ pendingRestart: true })} />)).toContain('Restart Survival to use them.')
  })
})

describe('Overview', () => {
  it('shows the first steps with the next one to do', async () => {
    const text = await render(<Overview server={server({ firstSteps: { invited: 'mara_k', friendJoined: 'mara_k', friendJoinedAt: new Date().toISOString(), backedUp: false, downloaded: false } })} />)
    expect(text).toContain('Survival is up. 2 small steps left.')
    expect(text).toContain('2 of 4 done')
    expect(text).toContain('Back up now')
    expect(text).toContain('mara_k is on the allowlist.')
  })

  // Regression for items 62 and 86: after a failed create the steps must
  // point at the step the job failed in, not at the server's own phase.
  it('marks the step a failed create stopped at', async () => {
    const s = server({ phase: 'stopped', startedAt: undefined, lastOperation: failed('create', 'downloading_server', 'The download did not match its checksum.') })
    const text = await render(<Overview server={s} />)
    expect(text).toContain('Setting up Survival didn’t finish')
    expect(text).toContain('The download did not match its checksum.')
    const steps = [...document.querySelectorAll('ol > li')]
    expect(steps).toHaveLength(4)
    expect(steps[0]?.querySelector('.bg-primary')).not.toBeNull()
    expect(steps[1]?.querySelector('.text-destructive-foreground')?.textContent).toBe('Downloading Paper 26.1.2')
    expect(steps[2]?.querySelector('.text-destructive-foreground')).toBeNull()
  })

  it('offers more memory after running out of it', async () => {
    answer({ '/logs': { epoch: 'e', lines: [{ seq: 1, ts: '2026-09-25T18:52:57Z', text: '[18:52:57 ERROR]: java.lang.OutOfMemoryError: Java heap space' }], next: 1, truncated: false }, '/catalog': { memoryOptionsMB: [2048, 3072, 4096, 6144, 8192], maxMemoryMB: 8192, versions: [], types: [], servers: [] } })
    const text = await render(<Overview server={server({ phase: 'crashed', crashCount: 2, exitCode: 1 })} />)
    expect(text).toContain('It ran out of its 4 GB of memory.')
    expect(text).toContain('Give Survival 6 GB')
    expect(text).toContain('Fits in the 10.5 GB free')
  })

  it('says which file stopped a start and what to do, in one line', async () => {
    const refusal = { code: 'link' as const, params: { path: 'plugins/bStats/config.yml' }, message: 'plugins/bStats/config.yml in the server’s files is a link, which Playkeeper does not follow.', hint: 'Delete it.' }
    const lastOperation = failed('start', '', 'Paper’s bStats usage statistics could not be switched off, so the server was not started. ' + refusal.message)
    const text = await render(<Overview server={server({ phase: 'stopped', startedAt: undefined, exitCode: 0, lastOperation, refusal })} />)
    expect(text).toContain('Playkeeper won’t start Survival while plugins/bStats/config.yml is a link. Delete it, or replace it with what it points to.')
    expect(text).toContain('Once plugins/bStats/config.yml is fixed, start Survival.')
    expect(text).not.toContain('bStats usage statistics')
    expect(text).not.toContain('Last lines before it stopped')
    const pipe = { ...refusal, code: 'special_file' as const, params: { path: 'plugins/bStats/config.yml', type: 'named_pipe' } }
    expect(await render(<Overview server={server({ phase: 'stopped', refusal: pipe })} />)).toContain('while plugins/bStats/config.yml isn’t a normal file. Delete it.')
    const dockerDown = await render(<Overview server={server({ phase: 'docker_unavailable', lastError: 'Docker is not responding, so Playkeeper cannot see or control the server.', refusal })} />)
    expect(dockerDown).toContain('Docker is not responding')
    expect(dockerDown).not.toContain('Playkeeper won’t start Survival')
  })
})

describe('Server settings', () => {
  it('turns down an icon over 64 KB next to the upload, without sending it', async () => {
    await render(<ServerSettingsPage server={server()} />)
    const input = document.querySelector<HTMLInputElement>('input[type=file]')
    if (!input) throw new Error('no icon upload')
    Object.defineProperty(input, 'files', { value: [new File([new Uint8Array(64 * 1024 + 1)], 'logo.png', { type: 'image/png' })] })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    expect(document.querySelector('[role=alert]')?.textContent).toBe('Icons need to be 64 × 64 and under 64 KB.')
    expect(client.api).not.toHaveBeenCalled()
  })
})

describe('Players', () => {
  // Like item 70: a session that ended in a crash has no exact length, so
  // its playtime is an estimate and says so.
  it('marks playtime that includes a crash-ended session as an estimate', async () => {
    const summary: PlayersSummary = {
      tz: 'UTC',
      days: [],
      players: [
        { name: 'Lenn0x', lastSeen: '2026-09-24T21:14:00Z', online: false, sessions: 5, playtimeSeconds: 6 * 3600 + 600, playtimeUncertain: true },
        { name: 'mara_k', lastSeen: '2026-09-25T10:00:00Z', online: false, sessions: 3, playtimeSeconds: 3600 },
      ],
      observedSessions: 8,
      uncertainSessions: 1,
      retentionDays: 180,
    }
    answer({ '/whitelist': [{ name: 'Lenn0x' }, { name: 'mara_k' }], '/operators': [], '/players/summary': summary, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('≈ 6 h 10 m')
    expect(text).toContain('1 hour')
    expect(text).not.toContain('≈ 1 hour')
    expect(text).toContain('≈ means it ended in a crash.')
  })

  it('shows a new player at once and puts the name back if Minecraft refuses it', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    let refuse: (e: unknown) => void = () => {}
    vi.mocked(client.post).mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          refuse = reject
        }),
    )
    await render(<PlayersPage server={server()} />)
    const field = () => document.querySelector('input[aria-label="Minecraft username"]') as HTMLInputElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(field(), 'tobi2009')
      field().dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => field().form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(document.body.textContent).not.toContain('Nobody’s joined yet')
    expect(document.querySelector('li')?.textContent).toContain('tobi2009')
    expect(field().value).toBe('')
    await act(async () => refuse(new client.ApiError(422, { error: 'That player does not exist', code: 'invalid' })))
    expect(document.body.textContent).toContain('Nobody’s joined yet')
    expect(field().value).toBe('tobi2009')
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('That player does not exist')
  })

  it('shows rows shaped like players until the lists arrive', async () => {
    const text = await render(<PlayersPage server={server()} />)
    expect(text).not.toContain('Nobody’s joined yet')
    expect(text).not.toContain('Nobody has played yet.')
    expect(text).not.toContain('0 people')
    expect(text).toContain('Loading…')
    expect(document.querySelectorAll('li [data-slot="skeleton"]').length).toBeGreaterThan(0)
  })

  it('explains how to invite someone when nobody has joined', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('Nobody’s joined yet')
    expect(text).toContain('You add their name')
  })
})

describe('World', () => {
  it('lists a new backup as soon as it is made, not at the next check', async () => {
    const at = new Date().toISOString()
    const job: Operation = { id: 'backup-1', kind: 'backup', status: 'running', phase: 'archiving', actor: 'siya', startedAt: at }
    const backup: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya', note: 'Before the dragon' }
    answer({ '/backups': [] })
    const text = await render(<WorldPage server={server({ phase: 'stopped', operation: job })} />)
    expect(text).toContain('No backups yet')
    expect(text).toContain('Backing up…')
    answer({ '/backups': [backup] })
    const done = server({ phase: 'stopped', lastOperation: { ...job, status: 'succeeded', finishedAt: at } })
    await act(async () =>
      root?.render(
        <WorkspaceContext.Provider value={workspace({ servers: [done] })}>
          <WorldPage server={done} />
        </WorkspaceContext.Provider>,
      ),
    )
    await act(async () => {})
    expect(document.body.textContent).not.toContain('No backups yet')
    expect(document.body.textContent).toContain('Before the dragon')
  })
})

describe('Get started', () => {
  it('follows the server with steps left', async () => {
    const text = await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ servers: [server({ firstSteps: { invited: 'mara_k', backedUp: false, downloaded: false } })] }))
    expect(text).toContain('1 of 4')
    expect(text).toContain('Next: Make your first backup')
  })

  it('hides once the owner hid it', async () => {
    expect(await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ prefs: { 'checklist.hidden.abcdefghjk': '1' } }))).toBe('')
  })

  it('hides under a key the panel accepts', () => {
    for (const s of [server(), undefined]) expect(hiddenKey(s)).toMatch(/^[a-z][a-z0-9.:_-]{0,63}$/)
  })

  it('hides at once and comes back if the panel refuses', async () => {
    let refuse: (e: unknown) => void = () => {}
    vi.mocked(client.post).mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          refuse = reject
        }),
    )
    const failed = vi.fn()
    function Hide() {
      const { prefs, setPrefs } = useWorkspace()
      return (
        <button type="button" onClick={() => void setPrefs({ [hiddenKey(server())]: '1' }).catch(failed)}>
          {Object.keys(prefs).join(',')}
        </button>
      )
    }
    await render(
      <WorkspaceProvider me={me} onSignedOut={() => {}}>
        <Hide />
      </WorkspaceProvider>,
    )
    const button = document.querySelector('button') as HTMLButtonElement
    await act(async () => button.click())
    expect(button.textContent).toBe('checklist.hidden.abcdefghjk')
    await act(async () => refuse(new client.ApiError(0, { error: 'The dashboard can’t be reached.', code: 'internal' })))
    expect(button.textContent).toBe('')
    expect(failed).toHaveBeenCalled()
  })
})

describe('Onboarding', () => {
  it('checks the machine in plain words, with the provider firewall to do by hand', async () => {
    const preflight: Preflight = {
      ok: true,
      checks: [
        { id: 'docker', label: 'Docker', status: 'pass', detail: 'Docker 27.3.1 is running.' },
        { id: 'memory', label: 'Memory', status: 'pass', detail: '16.0 GB RAM.' },
        { id: 'disk', label: 'Disk space', status: 'pass', detail: '41.0 GB free.' },
        { id: 'port', label: 'Game port', status: 'pass', detail: 'Port 25565 is free for Minecraft players.' },
        { id: 'egress', label: 'Download access', status: 'pass', detail: 'PaperMC is reachable.' },
      ],
    }
    answer({ '/preflight': preflight })
    const text = await render(<Onboarding />, workspace({ servers: [] }))
    expect(text).toContain('Checking this VPS')
    expect(text).toContain('Memory: 16 GB')
    expect(text).toContain('Ubuntu 24.04 on x86-64')
    expect(text).toContain('Port 25565 is free')
    expect(text).toContain('Your provider’s firewall')
    expect(text).toContain('6 of 7 look good')
  })
})

describe('Command palette', () => {
  it('keeps Tab and Shift+Tab inside the palette', async () => {
    await render(<CommandPalette open onOpenChange={() => {}} route={{ name: 'home' }} onShortcuts={() => {}} />)
    const palette = document.querySelector<HTMLElement>('[role="dialog"]')
    const search = palette?.querySelector<HTMLInputElement>('input[role="combobox"]')
    const shortcuts = [...(palette?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.includes('all shortcuts'))
    if (!search || !shortcuts) throw new Error('the palette has no search box or shortcuts button')
    const tab = async (from: HTMLElement, shiftKey: boolean) => {
      from.focus()
      await act(async () => {
        from.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true }))
      })
      return document.activeElement
    }
    expect(await tab(shortcuts, false)).toBe(search)
    expect(await tab(search, true)).toBe(shortcuts)
  })
})

describe('Machines and AI agents', () => {
  it('says a server’s controls wait for its machine while that machine is away', async () => {
    const away: MachineView = {
      id: 'a2345abcde',
      projectId: machine.projectId,
      name: 'attic',
      kind: 'remote',
      link: { machineId: 'a2345abcde', name: 'attic', fingerprint: 'X'.repeat(26), state: 'offline', lastSeen: new Date(Date.now() - 600_000).toISOString(), problems: [] },
    }
    const attic = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: away.id, lastKnownAt: new Date().toISOString() })
    const text = await render(<ServerPage slug="attic" tab="overview" />, workspace({ machines: [machine, away], servers: [server({ machineId: machine.id }), attic] }))
    expect(text).toContain('attic hasn’t called in for 10 minutes')
    const restart = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Restart'))
    expect(restart?.disabled).toBe(true)
    expect(restart?.title).toBe('Can’t reach attic')
    for (const tab of ['console', 'players', 'world', 'settings'] as const) {
      vi.mocked(client.get).mockClear()
      expect(await render(<ServerPage slug="attic" tab={tab} />, workspace({ machines: [machine, away], servers: [server({ machineId: machine.id }), attic] }))).toContain('attic hasn’t called in for 10 minutes')
      expect(vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes(attic.id)), `${tab} asks the away machine`).toEqual([])
    }
  })

  it('shows the not-answering view on every tab while a joined machine’s agent doesn’t answer, and asks it nothing', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      error: { error: 'The agent on home-server isn’t answering.', code: 'agent_unavailable' },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id, lastKnownAt: new Date(Date.now() - 120_000).toISOString() })
    const ws = workspace({ machines: [machine, home], servers: [server({ machineId: machine.id }), cobblemon] })
    const asked = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes(cobblemon.id) || p.includes(home.id))
    for (const tab of ['overview', 'console', 'players', 'world', 'settings'] as const) {
      vi.mocked(client.get).mockClear()
      const text = await render(<ServerPage slug="cobblemon" tab={tab} />, ws)
      expect(text, tab).toContain('Playkeeper can’t see your servers right now')
      expect(text, tab).toContain('Fix it on home-server')
      expect(asked(), `${tab} asks home-server`).toEqual([])
    }
    vi.mocked(client.get).mockClear()
    await render(<HomePage />, ws)
    expect(asked(), 'Home asks home-server for its activity').toEqual([])
    expect(vi.mocked(client.get).mock.calls.some(([p]) => String(p).includes(`/${machine.id}/activity`))).toBe(true)
  })

  it('asks a joined machine, not the dashboard’s, about the servers it runs', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'home-server', memoryTotalMB: 32768 },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id })
    const ws = workspace({ machines: [machine, home], servers: [server({ machineId: machine.id }), cobblemon] })
    const asked = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes('/catalog'))
    await render(<ServerSettingsPage server={cobblemon} />, ws)
    await render(<Overview server={{ ...cobblemon, phase: 'crashed', stoppedAt: new Date().toISOString() }} />, ws)
    expect(asked().length).toBeGreaterThan(1)
    expect(asked().every((p) => p.startsWith(`/api/machines/${home.id}/catalog`))).toBe(true)
  })

  it('says how to get a command when the dashboard has no address another machine can dial', async () => {
    forgetJoinCode()
    vi.mocked(client.post).mockClear()
    answer({ '/api/machines/link': { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5 }, sizingUrl: 'https://playkeeper.io/sizing', available: true, codes: [] } })
    const text = await render(<MachinesSection />)
    expect(text).toContain('Open this dashboard at its IP address or domain name, not localhost, to get the command.')
    expect(vi.mocked(client.post).mock.calls.some(([p]) => String(p).includes('/join-codes'))).toBe(false)
  })

  it('says why a token can’t be made yet', async () => {
    answer({ '/api/machines/link': { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5 }, sizingUrl: '', available: true }, '/api/tokens': [] })
    await render(<AiAgentsSection />)
    const open = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('New token'))
    await act(async () => open?.click())
    const make = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Make token')
    expect(make?.disabled).toBe(true)
    expect(make?.title).toBe('Give the token a name first.')
  })
})
