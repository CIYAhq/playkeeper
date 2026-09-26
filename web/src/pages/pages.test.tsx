// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { AddonSources, Backup, Catalog, MachineView, Me, ModpackDetail, ModpackResults, Operation, PlayersSummary, Preflight, RestorePreview, ServerConfig, ServerStatus, TemplateContents, TemplateExport, TemplatePlan } from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { AddonSourcesCard } from '@/components/app/addon-sources'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { ModpackPicker } from '@/components/app/modpacks'
import { RestoreDialog } from '@/components/app/restore'
import { AppShell } from '@/components/app/shell'
import { TemplateDialog } from '@/components/app/templates'
import { HomePage } from './home'
import { createNote, NewServerPage } from './new-server'
import { Onboarding } from './onboarding'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { ServerSettingsPage } from './server/settings'
import { WorldPage } from './server/world'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
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
  vi.mocked(client.api).mockReset()
  vi.mocked(client.api).mockImplementation(() => new Promise(() => {}))
  window.history.replaceState(null, '', '/')
})

/** Ticks or unticks the checkbox whose label starts with the text. */
async function toggle(label: string) {
  const input = [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))?.querySelector('input[type="checkbox"]')
  if (!(input instanceof HTMLInputElement)) throw new Error(`no checkbox “${label}”`)
  await act(async () => input.click())
  await act(async () => {})
}

/** Clicks the button whose text is exactly the label. */
async function click(label: string) {
  const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)
  if (!button) throw new Error(`no button “${label}”`)
  await act(async () => button.click())
  await act(async () => {})
}

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

  it('says when the agent stopped answering, keeping names but not numbers', async () => {
    const text = await render(<HomePage />, workspace({ agentDown: true, stale: true, servers: [server({ players: { online: 3, max: 10, names: [], source: '', at: '' } })] }))
    expect(text).toContain('Playkeeper can’t see your servers right now')
    expect(text).toContain('Survival')
    expect(text).toContain('No live status')
    expect(text).toContain('1 server on my-vps')
    expect(text).not.toContain('playing')
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
    expect(steps[1]?.textContent).not.toContain('Checksum matched')
    expect(text).not.toContain('Checksum matched')
  })

  it('keeps a template’s skipped add-on on the Overview after setup, with Try again', async () => {
    const skipped = [{ kind: 'plan_changed', params: { name: 'ViaRewind' }, message: 'What this would do has changed since you confirmed it.' }]
    const s = server({ config: { ...config, template: { name: 'Paper check', skipped } } })
    vi.mocked(client.post).mockClear()
    const text = await render(<Overview server={s} />)
    expect(text).toContain('ViaRewind from the template isn’t installed')
    expect(text).toContain('What this would do has changed since you confirmed it.')
    await click('Try again')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/template/retry', {})

    const busy = server({ config: s.config, operation: { id: 'op-1', kind: 'template-retry', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: new Date().toISOString() } })
    await render(<Overview server={busy} />)
    const retry = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Try again'))
    expect(retry?.disabled).toBe(true)
    expect(retry?.title).toBe('Installing Survival’s template add-ons. Try again when it’s done.')

    const two = [...skipped, { kind: 'pack_hash_mismatch', params: { name: 'Terralith' }, message: 'Terralith doesn’t match.' }]
    expect(await render(<Overview server={server({ config: { ...config, template: { name: 'Paper check', skipped: two } } })} />)).toContain('ViaRewind and Terralith from the template aren’t installed')
  })

  it('counts a modpack’s files as each one is checked', async () => {
    const at = new Date().toISOString()
    const s = server({
      name: 'Cobblemon',
      type: 'fabric',
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, minecraftVersion: '26.1.2', software: { type: 'fabric', minecraftVersion: '26.1.2', fabricLoader: '0.17.2' }, modpack: { source: 'modrinth', projectId: 'TPK00001', versionId: 'TPV00001', name: 'Cobblemon Modpack', versionNumber: '1.0.0', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_modpack', actor: 'siya', startedAt: at, detail: { packFiles: 71, packFilesTotal: 112 } },
    })
    const text = await render(<Overview server={s} />)
    expect(text).toContain('About 5 minutes. You can leave this page.')
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps).toHaveLength(5)
    expect(steps[1]).toContain('Downloaded Fabric for 26.1.2 and Fabric Loader 0.17.2')
    expect(steps[2]).toContain('Downloading the modpack’s mods')
    expect(steps[2]).toContain('71 of 112 files · each one checked')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('63')
  })

  it('counts a template’s plugins and names the ones it skipped', async () => {
    const at = new Date().toISOString()
    const skipped = [{ kind: 'addon_unsupported', params: { name: 'Simple Voice Chat' }, message: 'Simple Voice Chat has no version for Paper 26.1.2.' }]
    const s = server({
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, template: { name: 'Survival with friends', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: at, detail: { addons: 2, addonsTotal: 5, skipped } },
    })
    await render(<Overview server={s} />)
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps).toHaveLength(5)
    expect(steps[1]).toContain('Downloaded Paper 26.1.2 and checked it')
    expect(steps[2]).toContain('Downloading the template’s plugins')
    expect(steps[2]).toContain('2 of 5 files · each one checked · 1 skipped: Simple Voice Chat')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('40')
  })

  it('names a template’s data packs when it brings no plugins', async () => {
    const at = new Date().toISOString()
    const skipped = [{ kind: 'pack_hash_mismatch', params: { name: 'Terralith' }, message: 'The data pack Terralith doesn’t match the template’s checksum, so it wasn’t used.' }]
    const s = server({
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, template: { name: 'Survival with friends', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: at, detail: { packs: 1, packsTotal: 2, skipped } },
    })
    await render(<Overview server={s} />)
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps[2]).toContain('Downloading the template’s data packs')
    expect(steps[2]).toContain('1 of 2 files · each one checked · 1 skipped: Terralith')
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

describe('Templates', () => {
  const contents: TemplateContents = {
    name: 'Survival with friends',
    type: 'paper',
    minecraftVersion: '26.1.2',
    settings: { difficulty: 'normal', pvp: false, viewDistance: 10, maxPlayers: 10, memoryMB: 3072 },
    addons: [
      { source: 'modrinth', name: 'Chunky', versionNumber: '1.4.40' },
      { source: 'hangar', name: 'LuckPerms', versionNumber: 'v5.5.0' },
    ],
    resourcePacks: 0,
    dataPacks: 0,
    packs: [],
  }
  const plan: TemplatePlan = { contents, type: 'paper', versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 3072, skipped: [], warnings: [], blockers: [], ready: true, fingerprint: 'fp-1' }
  const catalog: Catalog = { type: 'paper', types: [], versions: [], memoryOptionsMB: [2048, 3072, 4096], recommendedMemoryMB: 2048, hostMemoryMB: 16384, maxMemoryMB: 4096, systemReserveMB: 1536, memoryFreeMB: 10752, servers: [], image: '' }

  it('starts a server from a shared link, sending only the plan the user saw', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue(plan)
    vi.mocked(client.post).mockClear()
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    const [method, path, , body] = vi.mocked(client.api).mock.calls[0] ?? []
    expect([method, path]).toEqual(['POST', '/api/machines/m2345abcde/templates/plan'])
    expect(await (body as Blob).text()).toBe('eyJ2IjoxfQ')
    expect(window.location.hash).toBe('')
    const text = document.body.textContent ?? ''
    expect(text).not.toContain('· from')
    for (const line of ['Survival with friends', 'From a link', 'Paper 26.1.2', '2, with the same versions', 'Chunky, LuckPerms', 'Normal difficulty · friends can’t hurt each other · view distance 10 · up to 10 players', 'The new server starts with fresh land', 'Paper, from the template', 'Type, version and plugins come from the template']) expect(text).toContain(line)

    await click('Continue to memory')
    expect(document.body.textContent).toContain('The template suggests 3 GB.')
    await click('Continue to name')
    await toggle('I accept the Minecraft End User License Agreement')
    await click('Create and start Survival with friends')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/machines/m2345abcde/servers', { name: 'Survival with friends', acceptEula: true, memoryMB: 3072, acceptExperimental: false, template: { fingerprint: 'fp-1' } })
  })

  it('says who made a template and when, when it says both', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue({ ...plan, contents: { ...contents, author: 'siya', created: '2026-09-25' } })
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    expect(document.body.textContent).toMatch(/Survival with friendsFrom a link · from siya · made (25 Sep|Sep 25)/)
  })

  it('shows the new plan when the machine no longer has the one the user saw', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValueOnce(plan).mockResolvedValueOnce({ ...plan, fingerprint: 'fp-2' })
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(409, { code: 'plan_changed', error: 'Playkeeper no longer has that template.', hint: 'Choose it again.' }))
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    await click('Continue to memory')
    await click('Continue to name')
    await toggle('I accept the Minecraft End User License Agreement')
    await click('Create and start Survival with friends')
    expect(vi.mocked(client.api)).toHaveBeenCalledTimes(2)
    const text = document.body.textContent ?? ''
    expect(text).toContain('Playkeeper no longer has that template.')
    expect(text).toContain('Check it again, then continue.')
    expect(text).toContain('Chunky, LuckPerms')
  })

  it('says what a template carries and offers the file when the link is too long', async () => {
    const link = `https://playkeeper.io/t#${'a'.repeat(2376)}`
    const exported: TemplateExport = {
      fileName: 'survival.playkeeper-template',
      file: 'x'.repeat(6000),
      link,
      linkLong: true,
      contents,
      available: { ...contents, settings: { difficulty: 'normal', pvp: false, viewDistance: 10, motd: 'Hi', maxPlayers: 10 } },
      packsHere: 1,
      leftOut: [{ kind: 'left_out_addon_upload', params: { name: 'MyPlugin' }, message: 'MyPlugin was uploaded, so it stays here.' }],
      notes: [],
    }
    answer({ '/template': exported })
    await render(<TemplateDialog server={server()} open onOpenChange={() => {}} />)
    const text = document.body.textContent ?? ''
    for (const line of ['Share Survival as a template', 'Paper 26.1.2 · always included', '2 plugins', 'Left out: MyPlugin', 'Difficulty, PvP, view distance, server list message and 1 more', 'Uploaded packs stay on this server', 'Pin exact versions', 'The same versions Survival runs', 'This link is 2,400 characters, too long for some chats', 'Send the file instead.', 'Download file · 6 KB']) expect(text).toContain(line)
    expect(vi.mocked(client.get)).toHaveBeenLastCalledWith('/api/servers/abcdefghjk/template')

    await toggle('Plugins')
    expect(vi.mocked(client.get)).toHaveBeenLastCalledWith('/api/servers/abcdefghjk/template?addons=off')
    expect(document.body.textContent).not.toContain('Pin exact versions')
  })
})

describe('Add-on sources', () => {
  const none: AddonSources = { curseforge: { key: 'none' } }
  const keyField = () => document.querySelector<HTMLInputElement>('input[aria-label="CurseForge API key"]')
  async function paste(value: string) {
    const input = keyField()
    if (!input) throw new Error('no key field')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)

  it('lands on its own section, with Modrinth and Hangar built in and CurseForge asking for a key', async () => {
    answer({ '/addon-sources': none })
    const text = await render(<AddonSourcesCard />)
    expect(document.getElementById('addon-sources')).not.toBeNull()
    expect(text).toContain('ModrinthOnBuilt in')
    expect(text).toContain('HangarOnBuilt in')
    expect(text).toContain('CurseForgeNeeds a key')
    for (const step of ['Sign in at console.curseforge.com', 'Open API keys and copy yours', 'Paste it here', 'The key stays on this machine.']) expect(text).toContain(step)
    expect(keyField()?.type).toBe('password')
    expect(button('Save key')?.disabled).toBe(true)
    expect(button('Save key')?.title).toBe('Paste a key first.')
  })

  it('says when CurseForge refuses the key, and keeps nothing', async () => {
    answer({ '/addon-sources': none })
    await render(<AddonSourcesCard />)
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(400, { code: 'curseforge_key_refused', error: "That key didn't work. Copy it again from console.curseforge.com." }))
    await paste('made-up-key-000000000000')
    await act(async () => button('Save key')?.closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await act(async () => {})
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/machines/m2345abcde/addon-sources/curseforge', { key: 'made-up-key-000000000000' })
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('That key didn’t work. Copy it again from console.curseforge.com.')
    expect(keyField()?.getAttribute('aria-invalid')).toBe('true')
    expect(document.body.textContent).not.toContain('Key ending')
  })

  it('shows a saved key by its ending, with Replace key and Remove', async () => {
    answer({ '/addon-sources': none })
    await render(<AddonSourcesCard />)
    vi.mocked(client.post).mockResolvedValueOnce({ curseforge: { key: 'file', ending: '3f9a' } })
    await paste(' pasted-key-0123456789abc3f9a ')
    await act(async () => button('Save key')?.closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await act(async () => {})
    const text = document.body.textContent ?? ''
    expect(text).toContain('CurseForgeOnKey ending 3f9a')
    expect(text).not.toContain('pasted-key')
    expect(button('Replace key')).toBeDefined()
    expect(button('Remove')).toBeDefined()
    expect(keyField()).toBeNull()
    await click('Replace key')
    expect(keyField()).not.toBeNull()
    expect(button('Cancel')).toBeDefined()
  })

  it('shows a key the release carries as on and built in, with nothing to change', async () => {
    answer({ '/addon-sources': { curseforge: { key: 'build' } } })
    const text = await render(<AddonSourcesCard />)
    expect(text).toContain('CurseForgeOnBuilt in')
    expect(keyField()).toBeNull()
    expect(button('Remove')).toBeUndefined()
  })

  it('lets only the owner change the key', async () => {
    answer({ '/addon-sources': { curseforge: { key: 'file', ending: '3f9a' } } })
    await render(<AddonSourcesCard />, workspace({ me: { ...me, user: { username: 'friend', role: 'member' } } }))
    for (const label of ['Replace key', 'Remove']) {
      expect(button(label)?.disabled).toBe(true)
      expect(button(label)?.title).toBe('Only the owner can change this.')
    }
  })
})

describe('Sidebar', () => {
  it('stops saying Creating once a create failed, like the server’s page', async () => {
    const failedCreate = failed('create', 'downloading_server', 'The server stopped while starting (exit code 1).')
    const text = await render(<AppShell route={{ name: 'home' }}><p>page</p></AppShell>, workspace({ servers: [server({ name: 'Modpack check', phase: 'stopped', startedAt: undefined, lastOperation: failedCreate })] }))
    const row = [...document.querySelectorAll('aside a')].find((a) => a.textContent?.includes('Modpack check'))
    expect(row?.textContent).toContain('Stopped')
    expect(text).not.toContain('Creating')
    expect(row?.querySelector('[data-slot="spinner"], .animate-spin')).toBeNull()
  })
})

describe('Modpacks', () => {
  const results: ModpackResults = {
    cards: [{ source: 'modrinth', projectId: 'SMTH0001', slug: 'smoothserver', name: 'Smooth Server', summary: 'Runs smoothly.', downloads: 1200, updated: '2026-09-01T00:00:00Z', pageUrl: 'https://modrinth.com/modpack/smoothserver', types: ['fabric'], minecraftVersions: ['26.2', '26.1'], mods: 17, memoryMB: 4096 }],
    total: 1,
    offset: 0,
    limit: 12,
    sources: ['modrinth'],
  }

  it('searches when Enter is pressed, and only then or after typing stops', async () => {
    vi.useFakeTimers()
    try {
      answer({ '/modpacks?': results })
      await render(<ModpackPicker machineId="m2345abcde" onChange={() => {}} onUse={() => {}} phone={false} />)
      const form = document.querySelector('form')
      const input = form?.querySelector('input')
      if (!form || !input) throw new Error('no search form')
      expect(form.querySelectorAll('input')).toHaveLength(1)
      const searched = () => vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes('q=simply+optimized')).length
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'simply optimized')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
      expect(searched()).toBe(0)
      await act(async () => {
        form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      })
      expect(searched()).toBe(1)
      await act(async () => {
        input.dispatchEvent(new FocusEvent('blur'))
        vi.advanceTimersByTime(500)
      })
      expect(searched()).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('says why a pack can’t be used, on the button and above it', async () => {
    const card = results.cards[0]
    if (!card) throw new Error('no card')
    const detail: ModpackDetail = { ...card, versions: [{ id: 'SMV00012', number: '1.2', channel: 'release', published: '2026-09-01T00:00:00Z', size: 16_000_000, type: 'fabric', minecraftVersion: '26.2', mods: 17 }], newest: 'SMV00012' }
    const blocker = { kind: 'file_too_large', message: 'Smooth Server has a file larger than Playkeeper accepts.' }
    answer({ '/preview': { type: 'fabric', minecraftVersion: '26.2', loaderVersion: '0.19.3', files: 17, downloadSize: 16_000_000, ready: false, blockers: [blocker], warnings: [], manual: [] }, '/modpacks/modrinth/SMTH0001': detail, '/modpacks?': results })
    await render(<ModpackPicker machineId="m2345abcde" onChange={() => {}} onUse={() => {}} phone={false} />)
    const row = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Smooth Server'))
    await act(async () => row?.click())
    await act(async () => {})
    await act(async () => {})
    const use = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Use this modpack')
    expect(use?.disabled).toBe(true)
    expect(use?.title).toBe(blocker.message)
    expect(document.body.textContent).toContain('Playkeeper can’t set up this pack')
  })

  it('says what a pack’s server downloads, not Paper', () => {
    expect(createNote(4, 'modpack', 'fabric')).toBe('After you start it, Playkeeper downloads Fabric and the pack’s mods, checks each file, and tells you when friends can join.')
    expect(createNote(4, 'template', 'neoforge')).toContain('downloads NeoForge and the template’s add-ons')
    expect(createNote(4, 'modpack', '')).not.toContain('Paper')
    expect(createNote(4, 'type', 'purpur')).toContain('downloads Purpur, checks it')
    expect(createNote(0, 'modpack', 'fabric')).toBe('Friends install the same modpack. You get a link to send.')
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

// Follow-ups after 0.3.0.
describe('World', () => {
  const click = async (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)
    if (!button) throw new Error(`no ${label} button`)
    await act(async () => button.click())
  }

  it('shows the newest world copy a restore left, and discards it only after asking', async () => {
    const copies = [
      { name: 'data.failed-restore-20260925-101500', kind: 'failed_restore', createdAt: '2026-09-25T10:15:00Z', sizeBytes: 1100 * 2 ** 20 },
      { name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 900 * 2 ** 20 },
    ]
    answer({ '/world-copies': copies, '/backups': [] })
    vi.mocked(client.del).mockClear()
    const text = await render(<WorldPage server={server()} />)
    expect(text).toContain('A restored world that didn’t start is still on this VPS')
    expect(text).toContain('1.1 GB')
    expect(text).not.toContain('Your previous world')
    await click('Discard')
    expect(client.del).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Discard this world copy?')
    answer({ '/world-copies': copies.slice(1), '/backups': [] })
    await click('Discard copy')
    expect(client.del).toHaveBeenCalledWith('/api/servers/abcdefghjk/world-copies/data.failed-restore-20260925-101500')
    expect(document.body.textContent).toContain('Your previous world is still on this VPS')
  })

  it('says why a copy can’t be discarded while a job runs', async () => {
    answer({ '/world-copies': [{ name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 2 ** 30 }], '/backups': [] })
    const job: Operation = { id: 'restore-1', serverId: 'abcdefghjk', kind: 'restore', status: 'running', phase: 'swapping', actor: 'siya', startedAt: '2026-09-25T10:00:00Z' }
    await render(<WorldPage server={server({ operation: job })} />)
    const discard = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Discard')
    expect(discard?.disabled).toBe(true)
    expect(discard?.title).toBe('Restoring Survival. Try again when it’s done.')
  })

  it('shows nothing when no restore left a copy', async () => {
    answer({ '/world-copies': [], '/backups': [] })
    expect(await render(<WorldPage server={server()} />)).not.toContain('still on this VPS')
  })
})

describe('Restore as a new server', () => {
  const preview: RestorePreview = {
    id: 'r2345abcde',
    source: 'Uploaded file world.tar.gz',
    receivedAt: '2026-09-25T10:00:00Z',
    sizeBytes: 50 * 2 ** 20,
    sha256: 'ab'.repeat(32),
    manifest: { createdAt: '2026-09-24T09:00:00Z', playkeeperVersion: '0.3.0', minecraftVersion: '26.1.2', paperBuild: 74, versionId: 'paper-26.1.2', levelName: 'world', fileCount: 89, totalBytes: 120 * 2 ** 20, sourceInstall: 'a1b2c3', settings: {} },
    compatible: true,
    problems: [],
    warnings: ['This backup was made on a different Playkeeper host.'],
    currentWorld: { exists: false, sizeBytes: 0 },
    willCreateRollback: false,
    needsEula: true,
    memoryMB: 1536,
    confirmPhrase: 'restore',
    steps: ['Install the world "world" from the backup'],
    notRestored: [],
  }

  it('asks for the EULA only until its box is ticked', async () => {
    const text = await render(<RestoreDialog preview={preview} onClose={() => {}} />)
    expect(text).toContain('you must accept the Minecraft EULA first')
    const box = document.querySelector<HTMLElement>('[role="checkbox"]')
    const label = box?.closest('label')
    if (!box || !label) throw new Error('no EULA checkbox')
    await act(async () => label.click())
    expect(box.getAttribute('aria-checked')).toBe('true')
    expect(document.body.textContent).not.toContain('you must accept the Minecraft EULA first')
    expect(document.body.textContent).toContain('This backup was made on a different Playkeeper host.')
  })
})
