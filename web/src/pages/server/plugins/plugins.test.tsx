// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Addon, AddonBrowse, AddonCard, AddonChecks, AddonDetails, AddonPlan, AddonRemovePreview, AddonStep, Addons, MachineView, Me, Operation, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import type { ServerSub } from '@/lib/router'
import { PluginsPage } from '.'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const me: Me = { user: { username: 'siya', role: 'owner' }, csrfToken: 't', expiresAt: '2026-09-26T00:00:00Z', idleTimeoutSeconds: 43200, version: '0.3.0' }
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 4096, heapMB: 3072, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true } as ServerConfig

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
    config,
    firstSteps: { backedUp: false, downloaded: false },
    ...over,
  }
}

function workspace(): Workspace {
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
  }
}

function addon(name: string, over: Partial<Addon> = {}): Addon {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return { source: 'modrinth', projectId: id, slug: id, name, summary: `${name} does things`, versionId: `${id}-1`, versionNumber: '1.0.0', channel: 'release', published: '2026-09-01T00:00:00Z', fileName: `${name}.jar`, size: 1000, installedAt: '2026-09-02T00:00:00Z', ...over }
}

const chunky = addon('Chunky', { source: 'hangar', projectId: '81', versionNumber: '1.4.36' })
const core = addon('Multiverse-Core', { versionNumber: '5.0.2' })
const protect = addon('CoreProtect', { versionNumber: '23.1' })
const voice = addon('Simple Voice Chat', { versionNumber: '2.5.26' })
const gone = addon('LuckPerms', { versionNumber: '5.4.150' })

const target: Addons['target'] = { kind: 'plugin', folder: 'plugins', sources: ['modrinth', 'hangar'], categories: ['admin', 'world'], minecraftVersion: '26.1.2' }

const installed: Addons = {
  target,
  files: [
    { fileName: chunky.fileName, size: 1, status: 'managed', addon: chunky },
    { fileName: core.fileName, size: 1, status: 'managed', addon: core, pending: true },
    { fileName: protect.fileName, size: 1, status: 'managed', addon: protect },
    { fileName: voice.fileName, size: 1, status: 'modified', addon: voice },
    { fileName: 'Vault.jar', size: 1, status: 'unknown', name: 'Vault', version: '1.7.3' },
    { fileName: 'homes.jar', size: 1, status: 'unknown', name: 'Homes', version: '0.9' },
  ],
  missing: [gone],
  warnings: [],
  restartNeeded: true,
}

const version = (n: string) => ({ versionId: `v-${n}`, versionNumber: n, channel: 'release', published: '2026-09-20T00:00:00Z' })

const checks: AddonChecks = {
  updates: [
    { source: 'hangar', projectId: '81', available: true, latest: version('1.4.40') },
    { source: 'modrinth', projectId: 'coreprotect', available: true, latest: version('23.2') },
    { source: 'modrinth', projectId: 'simplevoicechat', available: true, latest: version('2.6.0') },
  ],
  identified: [{ fileName: 'Vault.jar', size: 1, status: 'identified', addon: addon('Vault', { versionNumber: '1.7.3' }) }],
  checkedAt: '2026-09-25T00:00:00Z',
}

function card(name: string, over: Partial<AddonCard> = {}): AddonCard {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return { source: 'modrinth', projectId: id, slug: id, name, summary: `${name} summary`, categories: [], downloads: 1000, updated: '2026-09-20T00:00:00Z', pageUrl: `https://modrinth.com/plugin/${id}`, installed: false, ...over }
}

/** Answers GETs by the first path fragment they contain; anything else never resolves. */
function answer(routes: [string, unknown][]) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = routes.find(([part]) => path.includes(part))
    return hit ? Promise.resolve(hit[1]) : new Promise(() => {})
  }) as typeof client.get)
}

/** Answers POSTs by the end of their path. */
function reply(routes: [string, (body: unknown) => unknown][]) {
  vi.mocked(client.post).mockImplementation(((path: string, body: unknown) => {
    const hit = routes.find(([end]) => path.endsWith(end))
    return Promise.resolve(hit ? hit[1](body) : {})
  }) as typeof client.post)
}

function step(name: string, versionNumber: string, was?: string, neededBy?: string): AddonStep {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return { action: was ? 'update' : 'install', source: 'modrinth', projectId: id, name, versionNumber, channel: 'release', fileName: `${name}.jar`, size: 1_400_000, was, neededBy }
}

const updated = [
  { source: 'hangar', projectId: '81' },
  { source: 'modrinth', projectId: 'coreprotect' },
]
const updatePlan: AddonPlan = { steps: [step('Chunky', '1.4.40', '1.4.36'), step('CoreProtect', '23.2', '23.1')], manual: [], blockers: [], warnings: [], ready: true, fingerprint: 'fp1' }

let root: Root | undefined

async function render(s: ServerStatus, tab: 'plugins' | 'mods' = 'plugins', sub?: ServerSub): Promise<string> {
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={workspace()}><PluginsPage server={s} tab={tab} sub={sub} /></WorkspaceContext.Provider>))
  await act(async () => {})
  await act(async () => {})
  return document.body.textContent ?? ''
}

function button(text: string): HTMLElement {
  const all = [...document.querySelectorAll<HTMLElement>('button, a')]
  const b = all.find((el) => el.textContent?.trim() === text) ?? all.find((el) => el.textContent?.includes(text))
  if (!b) throw new Error(`no button with ${text}`)
  return b
}

async function click(text: string): Promise<string> {
  await act(async () => button(text).click())
  await act(async () => {})
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
  vi.mocked(client.post).mockReset()
  vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
})

describe('Plugins tab', () => {
  it('lists what’s installed, each file’s state and the one restart line', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    const text = await render(server())
    expect(text).toContain('Plugins on Survival')
    expect(text).toContain('Restart Survival to load 1 new plugin')
    expect(text).toContain('Update all')
    expect(text).toContain('Update available · 1.4.40')
    expect(text).toContain('New')
    expect(text).toContain('Changed since it was installed')
    expect(text).toContain('Asks before updating')
    expect(text).toContain('File missing')
    expect(text).toContain('Reinstall')
    expect(text).toContain('Found on Modrinth')
    expect(text).toContain('Let Playkeeper manage it')
    expect(text).toContain('Not in the library')
    expect(text).toContain('homes.jar')
    expect(text.indexOf('Chunky')).toBeLessThan(text.indexOf('Vault'))
  })

  it('shows the heading at once and grey rows while the list loads', async () => {
    const text = await render(server())
    expect(text).toContain('Plugins on Survival')
    expect(text.match(/Loading…/g)).toHaveLength(1)
    expect(document.querySelectorAll('[data-slot=skeleton]').length).toBeGreaterThanOrEqual(8)
  })

  it('says why Update all, Restart now and Reinstall wait while another job runs', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    await render(server({ operation: { id: 'b1', kind: 'backup', status: 'running', phase: '', actor: 'siya', startedAt: '' } }))
    for (const name of ['Update all', 'Restart now', 'Reinstall']) {
      const b = button(name) as HTMLButtonElement
      expect(b.disabled).toBe(true)
      expect(b.title).toBe('Backing up Survival. Try again when it’s done.')
    }
  })

  it('updates all unchanged add-ons and follows the job', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    const op: Operation = {
      id: 'op1',
      kind: 'addon-update',
      status: 'running',
      phase: 'downloading',
      actor: 'siya',
      startedAt: '',
      detail: {
        files: [
          { name: 'Chunky', versionNumber: '1.4.40', was: '1.4.36', size: 1_400_000, received: 1_400_000, state: 'verified' },
          { name: 'CoreProtect', versionNumber: '23.2', was: '23.1', size: 1_400_000, received: 900_000, state: 'downloading' },
        ],
      },
    }
    reply([
      ['/addons/update/plan', () => updatePlan],
      ['/addons/update', () => op],
    ])
    await render(server())
    const text = await click('Update all')
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/addons/update/plan', { addons: updated, changed: undefined })
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/addons/update', { addons: updated, changed: undefined, fingerprint: 'fp1' })
    expect(text).toContain('Updating 2 plugins')
    expect(text).toContain('Downloaded Chunky 1.4.40')
    expect(text).toContain('Was 1.4.36 · checksum matched')
    expect(text).toContain('Downloading CoreProtect 23.2')
    expect(text).toContain('Was 23.1 · 0.9 of 1.3 MB')
    expect(text).toContain('Restart Survival to load them')
    expect(text).toContain('Keeps going if you close this.')
  })

  it('shows the planned files until the agent reports its own', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    reply([
      ['/addons/update/plan', () => updatePlan],
      ['/addons/update', () => ({ id: 'op1', kind: 'addon-update', status: 'running', phase: '', actor: 'siya', startedAt: '' })],
    ])
    await render(server())
    const text = await click('Update all')
    expect(text).toContain('Downloading Chunky 1.4.40')
    expect(text).toContain('Downloading CoreProtect 23.2')
    expect(text).not.toContain('Loading')
  })

  it('says plainly when the plan changed, and confirms the new one before updating', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    const changed = 'What this would do has changed since you confirmed it.'
    const refused: Operation = {
      id: 'op3',
      kind: 'addon-update',
      status: 'failed',
      phase: '',
      actor: 'siya',
      startedAt: '',
      error: changed,
      detail: { notice: { kind: 'plan_changed', message: changed, hint: 'Review the new plan and confirm again.' } },
    }
    const plans = [updatePlan, { ...updatePlan, steps: [...updatePlan.steps, step('Chunky Border', '1.2', undefined, 'Chunky')], fingerprint: 'fp2' }]
    reply([
      ['/addons/update/plan', () => plans.shift()],
      ['/addons/update', (body) => ((body as { fingerprint: string }).fingerprint === 'fp1' ? refused : { ...refused, id: 'op4', status: 'running', error: undefined, detail: undefined })],
    ])
    await render(server())
    let text = await click('Update all')
    expect(text).toContain(changed)
    expect(text).toContain('Review the new plan and confirm again.')
    text = await click('Look again')
    expect(text).toContain('Downloading Chunky Border 1.2')
    expect(client.post).toHaveBeenCalledTimes(3)
    text = await click('Update')
    expect(client.post).toHaveBeenLastCalledWith('/api/servers/abcdefghjk/addons/update', { addons: updated, changed: undefined, fingerprint: 'fp2' })
    expect(text).toContain('Keeps going if you close this.')
  })

  it('keeps the restart line for a new file the agent does not flag, and puts its update first', async () => {
    const newer: AddonChecks = { ...checks, updates: [...checks.updates, { source: 'modrinth', projectId: 'multiversecore', available: true, latest: version('5.1.0') }] }
    answer([
      ['/addons/checks', newer],
      ['/addons', { ...installed, restartNeeded: false }],
    ])
    const text = await render(server())
    expect(text).toContain('Restart Survival to load 1 new plugin')
    const row = [...document.querySelectorAll('li')].find((li) => li.textContent?.includes('Multiverse-Core'))
    expect(row?.textContent).toContain('Update available · 5.1.0')
    expect(row?.textContent).not.toContain('New')
  })

  it('reinstalls a missing file without calling the same version old', async () => {
    answer([
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    const op: Operation = {
      id: 'op2',
      kind: 'addon-update',
      status: 'running',
      phase: 'downloading',
      actor: 'siya',
      startedAt: '',
      detail: { files: [{ name: 'LuckPerms', versionNumber: '5.4.150', was: '5.4.150', size: 1000, received: 1000, state: 'verified' }] },
    }
    reply([
      ['/addons/update/plan', () => ({ ...updatePlan, steps: [step('LuckPerms', '5.4.150', '5.4.150')], fingerprint: 'fp4' })],
      ['/addons/update', () => op],
    ])
    await render(server())
    const text = await click('Reinstall')
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/addons/update', { addons: [{ source: 'modrinth', projectId: 'luckperms' }], changed: undefined, fingerprint: 'fp4' })
    expect(text).toContain('Installing LuckPerms')
    expect(text).toContain('Downloaded LuckPerms 5.4.150')
    expect(text).toContain('Checksum matched')
    expect(text).not.toContain('Was 5.4.150')
  })

  it('says nothing is installed yet', async () => {
    answer([['/addons', { ...installed, files: [], missing: [], restartNeeded: false }]])
    const text = await render(server())
    expect(text).toContain('No plugins yet')
    expect(text).toContain('Browse plugins')
  })

  it('calls them mods on mod servers', async () => {
    answer([['/addons', { ...installed, target: { ...target, kind: 'mod', folder: 'mods' }, files: [], missing: [] }]])
    const text = await render(server({ type: 'fabric' }), 'mods')
    expect(text).toContain('No mods yet')
    expect(text).toContain('Browse mods')
  })

  it('shows an installed add-on’s details with Update and Remove', async () => {
    const details: AddonDetails = {
      card: card('Chunky', { source: 'hangar', projectId: '81', author: 'pop4959', license: 'GPL-3.0', pageUrl: 'https://hangar.papermc.io/pop4959/Chunky' }),
      latest: version('1.4.40'),
      installed: chunky,
      updateAvailable: true,
    }
    answer([
      ['/addons/checks', checks],
      ['/addons/project/', details],
      ['/addons', installed],
    ])
    await render(server())
    const text = await click('Chunky')
    expect(text).toContain('by pop4959')
    expect(text).toContain('1.4.36, from Hangar')
    expect(text).toContain('1.4.40 for Paper 26.1.2')
    expect(text).toContain('What’s new in 1.4.40')
    expect(text).toContain('Update to 1.4.40')
    expect(text).toContain('Loads after a restart.')
    expect(document.querySelector('a[href="https://hangar.papermc.io/pop4959/Chunky/versions/1.4.40"]')).not.toBeNull()
  })

  it('offers Remove anyway when another plugin needs it', async () => {
    const details: AddonDetails = { card: card('Multiverse-Core'), latest: version('5.0.2'), installed: core }
    const preview: AddonRemovePreview = { addon: core, neededBy: ['Multiverse-Portals'], orphans: [], configFolder: 'plugins/Multiverse-Core', changed: false, missing: false }
    answer([
      ['/removal', preview],
      ['/addons/checks', checks],
      ['/addons/project/', details],
      ['/addons', installed],
    ])
    await render(server())
    await click('Multiverse-Core')
    const text = await click('Remove')
    expect(text).toContain('Remove Multiverse-Core?')
    expect(text).toContain('Multiverse-Portals needs it')
    expect(text).toContain('Keep its settings')
    expect(text).toContain('Keep Multiverse-Core')
    vi.mocked(client.post).mockImplementation((() => Promise.resolve({ removed: ['Multiverse-Core.jar'], warnings: [] })) as typeof client.post)
    await click('Remove anyway')
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/addons/remove', { source: 'modrinth', projectId: 'multiversecore', keepConfig: true, force: true, changed: undefined, orphans: undefined })
  })

  it('browses the library, marking what’s already installed', async () => {
    const browse: AddonBrowse = {
      cards: [card('BlueMap', { author: 'BlueColored', downloads: 2_100_000 }), card('Chunky', { source: 'hangar', projectId: '81', installed: true })],
      more: false,
      unanswered: [],
    }
    answer([
      ['/addons/checks', checks],
      ['/addons/search', browse],
      ['/addons', installed],
    ])
    const text = await render(server(), 'plugins', 'browse')
    expect(text).toContain('Browse plugins')
    expect(text).toContain('For Paper 26.1.2')
    expect(text).toContain('by BlueColored')
    expect(text).toContain('2.1M downloads')
    expect(text).toContain('Installed')
    expect(vi.mocked(client.get).mock.calls.some(([path]) => path === '/api/servers/abcdefghjk/addons/search')).toBe(true)
  })

  const orebfuscator: AddonDetails = {
    card: card('Orebfuscator', { source: 'hangar', projectId: 'Orebfuscator' }),
    latest: version('5.6.2'),
    plan: {
      steps: [step('Orebfuscator', '5.6.2')],
      manual: [{ kind: 'dependency_external', params: { name: 'Orebfuscator', dependency: 'ProtocolLib', folder: 'plugins' }, message: 'Orebfuscator needs ProtocolLib, which is not on Hangar.', url: 'https://github.com/dmulloy2/ProtocolLib/' }],
      blockers: [],
      warnings: [],
      ready: true,
      fingerprint: 'fp5',
    },
  }

  async function openNeeds(lookup: AddonCard[]): Promise<string> {
    answer([
      ['/addons/checks', checks],
      ['/addons/search?q=ProtocolLib', { cards: lookup, more: false, unanswered: [] }],
      ['/addons/search', { cards: [orebfuscator.card], more: false, unanswered: [] }],
      ['/addons/project/modrinth/protocollib', { card: card('ProtocolLib'), latest: version('5.4.0'), plan: { ...updatePlan, steps: [step('ProtocolLib', '5.4.0')], fingerprint: 'fp6' } }],
      ['/addons/project/', orebfuscator],
      ['/addons', installed],
    ])
    await render(server(), 'plugins', 'browse')
    await click('Orebfuscator')
    await act(async () => {})
    return document.body.textContent ?? ''
  }

  it('offers a dependency another site names when the library has it', async () => {
    const text = await openNeeds([card('ProtocolSupport'), card('ProtocolLib')])
    expect(text).toContain('Needs ProtocolLib first')
    expect(text).toContain('ProtocolLib is in the library. Install it, then come back here.')
    expect(text).not.toContain('isn’t in the library')
    expect(await click('Go to ProtocolLib')).toContain('Install ProtocolLib')
  })

  it('says a dependency another site names isn’t in the library only after looking', async () => {
    const text = await openNeeds([card('ProtocolSupport')])
    expect(vi.mocked(client.get).mock.calls.some(([path]) => path === '/api/servers/abcdefghjk/addons/search?q=ProtocolLib')).toBe(true)
    expect(text).toContain('ProtocolLib isn’t in the library. Add it by hand first.')
    expect(document.querySelector('a[href="https://github.com/dmulloy2/ProtocolLib/"]')).not.toBeNull()
  })
})
