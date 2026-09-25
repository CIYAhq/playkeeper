// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { MachineView, Me, OffsiteTestResult, OffsiteView, Operation, PlayersSummary, Preflight, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { GetStartedCard } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { HomePage } from './home'
import { Onboarding } from './onboarding'
import { CopiesCard } from './server/copies'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
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
    expect(text).toContain('1 server on my-vps')
    expect(text).toContain('3 people playing right now')
  })

  it('says when the agent stopped answering, keeping names but not numbers', async () => {
    const text = await render(<HomePage />, workspace({ agentDown: true, stale: true, servers: [server({ players: { online: 3, max: 10, names: [], source: '', at: '' } })] }))
    expect(text).toContain('Playkeeper can’t see your servers right now')
    expect(text).toContain('Survival')
    expect(text).toContain('No live status')
    expect(text).not.toContain('3 playing')
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
    const text = await render(<Overview server={server()} />, workspace({ machine: { ...machine, live: machine.live && { ...machine.live, diskWarning } } }))
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
    expect(text).toContain('It ran out of memory. Survival has 4 GB')
    expect(text).toContain('Give Survival 6 GB')
    expect(text).toContain('Playkeeper restarted it and it stopped each time')
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
    expect(text).toContain('≈ means a session ended in a crash')
  })

  it('explains how to invite someone when nobody has joined', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('Nobody’s joined yet')
    expect(text).toContain('You add their name')
  })
})

describe('Get started', () => {
  it('follows the server with steps left', async () => {
    const text = await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ servers: [server({ firstSteps: { invited: 'mara_k', backedUp: false, downloaded: false } })] }))
    expect(text).toContain('1 of 4')
    expect(text).toContain('Next: Make your first backup')
  })

  it('hides once the owner hid it', async () => {
    expect(await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ prefs: { 'firstSteps.hidden.abcdefghjk': '1' } }))).toBe('')
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

describe('Copies somewhere else', () => {
  const sftp: OffsiteView = {
    enabled: false,
    configured: true,
    type: 'sftp',
    place: 'vault.example.net',
    sftp: { host: 'vault.example.net', port: 22, user: 'playkeeper', folder: 'backups/survival', auth: 'key' },
    sshKey: { publicKey: 'ssh-ed25519 AAAA', authorizedKey: 'restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGm4bWJpbmFyeWtleWJ5dGVzZm9yYXRlc3Q1q7Rk playkeeper-survival', fingerprint: 'SHA256:x' },
    copies: 0,
    copiesBytes: 0,
    queued: 0,
    providers: [],
  }
  const hostKey = { key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey', type: 'ssh-ed25519', fingerprint: 'SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA' }
  afterEach(() => {
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  })
  const click = async (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(label))
    if (!button) throw new Error(`no button "${label}"`)
    await act(async () => button.click())
    await act(async () => {})
  }

  it('confirms a new host key, saves it and tests again before turning copies on', async () => {
    answer({ '/offsite': sftp })
    const unknown: OffsiteTestResult = { ok: false, skew: 0, hostKey, checks: [{ step: 'connect', ok: false, msg: 'Playkeeper has not seen this host key yet.', kind: 'host_key_unknown' }] }
    const passed: OffsiteTestResult = { ok: true, skew: 0, checks: ['connect', 'folder', 'write', 'rename', 'read', 'list', 'delete'].map((step) => ({ step, ok: true, msg: '' })) }
    const tests = [unknown, passed]
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/offsite/test') ? tests.shift() : sftp)) as typeof client.post)
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(document.body.textContent).toContain('Encrypted before they leave. Copies start once the test passes.')
    await click('Test connection')
    expect(document.body.textContent).toContain('Is this really vault.example.net?')
    expect(document.body.textContent).toContain(hostKey.fingerprint)
    expect(document.body.textContent).toContain('ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub')
    await click('It matches, confirm')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite', { hostKey: hostKey.key })
    const text = document.body.textContent ?? ''
    expect(text).toContain('All checks passed')
    expect(text).toContain('Connected and signed in as playkeeper')
    expect(text).toContain('Turn on copies')
  })

  it('stops copies when the host key changed and shows both fingerprints', async () => {
    const pinned = 'SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA'
    const now = 'SHA256:Zx81bQe4Wn7cHs2LmP0vA9tYd6KfR3gJuN5oE1iXwTk'
    answer({
      '/offsite': {
        ...sftp,
        enabled: true,
        key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, savedAt: '2026-09-24T10:05:00Z', fileName: 'playkeeper-recovery-key-survival.txt' },
        pending: { backupId: 'b1', fileName: 'b1.tar.zst', uploading: false, sent: 0, total: 1, attempts: 1, error: 'The key changed.', errorKind: 'host_key_changed', params: { fingerprint: now, pinnedFingerprint: pinned } },
      },
    })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(document.body.textContent).toContain('Stopped')
    expect(document.body.textContent).toContain('Copies stopped: vault.example.net’s key changed')
    expect(document.body.textContent).toContain('Downloaded')
    await click('Review')
    const text = document.body.textContent ?? ''
    expect(text).toContain('vault.example.net’s key changed')
    expect(text).toContain(pinned)
    expect(text).toContain(now)
    expect(text).toContain('Check the new key')
  })

  it('lets only the owner change where copies go or download the key', async () => {
    answer({ '/offsite': { ...sftp, enabled: true, key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, fileName: 'playkeeper-recovery-key-survival.txt' } } })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />, workspace({ me: { ...me, user: { username: 'friend', role: 'member' } } }))
    expect(document.body.textContent).toContain('Only the owner can change where copies go.')
    expect(document.querySelector<HTMLInputElement>('#offsite-host')?.disabled).toBe(true)
    const download = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Download')
    expect(download?.disabled).toBe(true)
  })
})
