// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, Addon, AddonBrowse, AddonCard, AddonChecks, AddonDetails, AddonPlan, AddonRemovePreview, AddonStep, Addons, Address, CuratedAddons, MachineView, Me, Operation, PackShare, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import type { ServerSub } from '@/lib/router'
import { PluginsPage } from '.'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
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
    reloadMe: async () => {},
    signInNotice: undefined,
    dismissSignInNotice: () => {},
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

const packShare: PackShare = {
  public: false,
  file: 'survival.mrpack',
  size: 2048,
  loaderName: 'Fabric',
  share: {
    server: 'Survival',
    type: 'fabric',
    minecraftVersion: '26.1.2',
    loaderVersion: '0.17.2',
    notice: { key: 'share.notice.one', params: { mod: 'Waystones' }, text: 'Friends need Waystones' },
    mods: [],
  },
}

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

  it('shows what the Map installed as the Map’s, linked to it, with nothing to manage', async () => {
    const squaremap = addon('squaremap', { projectId: 'PFb7ZqK6', versionNumber: '1.3.13.2', usedBy: 'map' })
    answer([
      ['/addons/checks', { updates: [], identified: [], checkedAt: '2026-09-25T00:00:00Z' }],
      ['/addons', { ...installed, files: [{ fileName: squaremap.fileName, size: 1, status: 'managed', addon: squaremap, pending: true }], missing: [], restartNeeded: false }],
    ])
    const text = await render(server())
    expect(text).toContain('squaremap1.3.13.2')
    expect(text).toContain('Used by the Map')
    expect(text).toContain('Modrinth')
    for (const hidden of ['By hand', 'Added by hand', 'Let Playkeeper manage it', 'Restart Survival', 'Remove']) expect(text).not.toContain(hidden)
    expect(document.querySelector('[aria-label="More actions for squaremap"]')).toBeNull()
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.includes('squaremap'))).toBe(false)
    expect(button('Used by the Map').getAttribute('href')).toBe('/servers/survival/map')
  })

  it('points to the Map from what it installed, without Remove or Update', async () => {
    const squaremap = addon('squaremap', { projectId: 'PFb7ZqK6', versionNumber: '1.3.13.2', usedBy: 'map' })
    const details: AddonDetails = { card: card('squaremap', { projectId: 'PFb7ZqK6', installed: true }), latest: version('1.3.14'), installed: squaremap }
    answer([
      ['/addons/checks', checks],
      ['/addons/search', { cards: [details.card], more: false, unanswered: [] }],
      ['/addons/project/', details],
      ['/addons', installed],
    ])
    await render(server(), 'plugins', 'browse')
    const text = await click('squaremap')
    expect(text).toContain('Used by the Map')
    expect(text).toContain('Turning off the map removes it.')
    expect(text).not.toContain('Update to 1.3.14')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Remove')).toBe(false)
    await click('Open the Map')
    expect(window.location.pathname).toBe('/servers/survival/map')
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

  it('offers Share with friends above a mod server’s mods, on desktop and phone', async () => {
    answer([
      ['/mods/share', packShare],
      ['/addons/checks', checks],
      ['/addons', { ...installed, target: { ...target, kind: 'mod', folder: 'mods' } }],
    ])
    let text = await render(server({ type: 'fabric' }), 'mods')
    expect(text).toContain('Friends need Waystones')
    expect(text).toContain('Send them one link to set it all up.')
    expect(text.indexOf('Friends need Waystones')).toBeLessThan(text.indexOf('Mods on Survival'))
    expect(button('Share with friends').tagName).toBe('BUTTON')

    await act(async () => root?.unmount())
    const media = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('max-width: 639px'), media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList,
    )
    text = await render(server({ type: 'fabric' }), 'mods')
    media.mockRestore()
    expect(text).toContain('Send them one link.')
    expect(button('Share with friends').tagName).toBe('BUTTON')
  })

  it('shares the pack under the machine’s name once it works there, and suggests a name first without one', async () => {
    const shared: PackShare = { ...packShare, public: true, token: 'Fake0Share0Token0Abcde' }
    const mods = { ...installed, target: { ...target, kind: 'mod' as const, folder: 'mods' } }
    const named = {
      kind: 'playkeeper',
      host: 'alex.playkeeper.io',
      panelPort: 8443,
      free: { name: 'alex', state: 'active', dns: 'ok', claimedAt: '2026-09-20T10:00:00Z', refreshedAt: '2026-09-25T10:00:00Z' },
      certificate: { notAfter: '2099-01-01T00:00:00Z' },
    } as Address
    const modded = server({ type: 'fabric', machineId: machine.id, joinAddress: 'survival.alex.playkeeper.io' })
    const link = () => [...document.querySelectorAll('input')].find((i) => i.value.includes('/packs/'))?.value
    answer([
      ['/mods/share', shared],
      ['/address', named],
      ['/addons/checks', checks],
      ['/addons', mods],
    ])
    await render(modded, 'mods')
    let text = await click('Share with friends')
    expect(link()).toBe('https://alex.playkeeper.io:8443/packs/Fake0Share0Token0Abcde')
    expect(text).toContain('Press Play, then join survival.alex.playkeeper.io.')
    expect(text).not.toContain('Set up an address first')

    await act(async () => root?.unmount())
    answer([
      ['/mods/share', shared],
      ['/address', { kind: '', panelPort: 8443 } as Address],
      ['/addons/checks', checks],
      ['/addons', mods],
    ])
    await render({ ...modded, joinAddress: undefined }, 'mods')
    text = await click('Share with friends')
    expect(link()).toBe(`${window.location.origin}/packs/Fake0Share0Token0Abcde`)
    expect(text).toContain('Set up an address first so the link keeps working if the machine’s IP changes.')
    expect(button('Machine settings').getAttribute('href')).toBe('/machines/m2345abcde/settings')
    expect(button('Copy link').tagName).toBe('BUTTON')
  })

  it('lists a modpack’s mods with the pack, not as added by hand', async () => {
    const pack = { source: 'modrinth' as const, projectId: 'TPK00001', versionId: 'TPV00001', name: 'Smooth Server', versionNumber: '1.2', mods: 2 }
    const modded: Addons = {
      ...installed,
      target: { ...target, kind: 'mod', folder: 'mods' },
      modpack: pack,
      files: [
        { fileName: chunky.fileName, size: 1, status: 'managed', addon: chunky },
        { fileName: 'lithium-0.18.jar', size: 1, status: 'pack', name: 'Lithium', version: '0.18.0' },
        { fileName: 'krypton-0.2.jar', size: 1, status: 'pack', name: 'Krypton', version: '0.2.9' },
      ],
      missing: [],
      restartNeeded: false,
    }
    const share: PackShare = {
      ...packShare,
      share: {
        ...packShare.share,
        pack: { name: 'Smooth Server', version: '1.2', source: 'modrinth', need: 'required', label: { key: 'share.need.required', text: 'Friends need it' } },
        mods: [{ name: 'Lithium', version: '0.18.0', path: 'mods/lithium-0.18.jar', from: 'pack', onServer: true, need: 'optional', label: { key: 'share.need.optional', text: 'Optional for friends' }, inFile: true }],
      },
    }
    answer([
      ['/mods/share', share],
      ['/addons/checks', { ...checks, identified: [] }],
      ['/addons', modded],
    ])
    let text = await render(server({ type: 'fabric' }), 'mods')
    expect(text).toContain('Added by you')
    expect(text).toContain('From the modpack')
    expect(text).toContain('Smooth Server')
    expect(text).toContain('2 mods · version 1.2')
    expect(text).toContain('Friends need it')
    expect(text).not.toContain('Added by hand')
    expect(text).not.toContain('Let Playkeeper manage it')
    expect(text).not.toContain('Lithium')
    text = await click('Show all 2')
    expect(text).toContain('Lithium0.18.0Optional for friends')
    expect(text).toContain('Krypton0.2.9')
    expect(text.indexOf('Krypton')).toBeLessThan(text.indexOf('Lithium'))
    expect(await click('Show fewer')).not.toContain('Lithium')
  })

  it('says what friends need of each mod added by hand, with the restart line under the heading', async () => {
    const waystones = addon('Waystones', { versionNumber: '21.1.4' })
    const chunkyMod = addon('Chunky', { versionNumber: '1.4.40' })
    const spark = addon('spark', { versionNumber: '1.10.124' })
    const mods: Addons = {
      target: { ...target, kind: 'mod', folder: 'mods' },
      files: [waystones, chunkyMod, spark].map((m) => ({ fileName: m.fileName, size: 1, status: 'managed' as const, addon: m })),
      missing: [],
      warnings: [],
      restartNeeded: false,
    }
    const label = (need: 'required' | 'optional' | 'server_only', text: string) => ({ need, label: { key: `share.need.${need}`, text } })
    const share: PackShare = {
      ...packShare,
      share: {
        ...packShare.share,
        mods: [
          { name: 'Waystones', path: 'mods/Waystones.jar', from: 'user', source: 'modrinth', project: waystones.projectId, onServer: true, inFile: true, ...label('required', 'Friends need it') },
          { name: 'Chunky', path: 'mods/Chunky.jar', from: 'user', source: 'modrinth', project: chunkyMod.projectId, onServer: true, inFile: true, ...label('optional', 'Optional for friends') },
          { name: 'spark', path: 'mods/spark.jar', from: 'user', source: 'modrinth', project: spark.projectId, onServer: true, inFile: false, ...label('server_only', 'Server only') },
        ],
      },
    }
    answer([
      ['/mods/share', share],
      ['/addons/checks', { ...checks, updates: [], identified: [] }],
      ['/addons', mods],
    ])
    let text = await render(server({ type: 'fabric' }), 'mods')
    expect(text).toContain('Mods on SurvivalChanges load after a restart.')
    expect(text).toContain('Waystones21.1.4')
    expect(text).toContain('Friends need it')
    expect(text).toContain('Optional for friends')
    expect(text).toContain('Server only')
    expect(text).not.toContain('Up to date')
    const need = [...document.querySelectorAll('li')].find((li) => li.textContent?.includes('Waystones'))
    expect(need?.querySelector('.text-success-foreground')?.textContent).toBe('Friends need it')

    await act(async () => root?.unmount())
    const media = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('max-width: 639px'), media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList,
    )
    text = await render(server({ type: 'fabric' }), 'mods')
    media.mockRestore()
    expect(text).toContain('1.4.40 · Optional for friends')
    expect(text).toContain('1.10.124 · Server only')
    expect(text).not.toContain('· Modrinth')
  })

  it('doesn’t offer Share with friends on a plugin server', async () => {
    answer([
      ['/mods/share', packShare],
      ['/addons/checks', checks],
      ['/addons', installed],
    ])
    const text = await render(server())
    expect(text).toContain('Plugins on Survival')
    expect(text).not.toContain('Share with friends')
    expect(vi.mocked(client.get).mock.calls.some(([path]) => String(path).includes('/mods/share'))).toBe(false)
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
    expect(text).not.toContain('For Paper 26.1.2')
    // Before a search the cards are the short ones: the summary under the name and where it's from, no author.
    expect(text).not.toContain('by BlueColored')
    expect(text).toContain('2.1M downloads · Modrinth')
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

  const svc = card('Simple Voice Chat', { projectId: '9eGKb6K1', slug: 'simple-voice-chat' })
  const picked: CuratedAddons = {
    picks: [
      { id: 'voice-chat', card: svc, ports: [{ protocol: 'udp', port: 24454 }] },
      { id: 'rollback', card: card('CoreProtect', { projectId: 'Lu3KuzdV' }) },
      { id: 'pregenerate', card: card('Chunky', { source: 'hangar', projectId: '81', installed: true }) },
      { id: 'permissions', card: card('LuckPerms', { projectId: 'Vebnzrzj' }) },
      { id: 'essentials', card: card('EssentialsX', { projectId: 'hXiIvTyT' }) },
    ],
  }
  const library: AddonBrowse = { cards: [card('BlueMap')], more: false, unanswered: [] }
  const none: Addons = { target, files: [], missing: [], warnings: [], restartNeeded: false }

  it('shows four of Playkeeper’s picks before a search, then Most downloaded', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      answer([
        ['/addons/checks', checks],
        ['/addons/curated', picked],
        ['/addons/search', library],
        ['/addons', none],
      ])
      const text = await render(server(), 'plugins', 'browse')
      expect(text).toContain('Picked by PlaykeeperHand-picked for Paper 26.1.2')
      for (const line of ['Hear friends nearby, quieter as they walk away.', 'Needs one more port. Friends add the mod to talk.', 'Undo griefing, block by block.', 'Groups decide who can use which commands.']) expect(text).toContain(line)
      expect(text).not.toContain('EssentialsX')
      const headings = [...document.querySelectorAll('h3')].map((h) => h.textContent)
      expect(headings).toEqual(['Picked by PlaykeeperHand-picked for Paper 26.1.2', 'Most downloaded'])
      expect(text.lastIndexOf('Most downloaded')).toBeLessThan(text.indexOf('BlueMap'))
      const search = document.querySelector<HTMLInputElement>('input[type="search"]')
      if (!search) throw new Error('no search field')
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(search, 'maps')
        search.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await act(async () => {
        vi.advanceTimersByTime(400)
      })
      await act(async () => {})
      expect(document.body.textContent).not.toContain('Picked by Playkeeper')
    } finally {
      vi.useRealTimers()
    }
  })

  it('asks before installing voice chat, then installs it and opens its port', async () => {
    const details: AddonDetails = {
      card: svc,
      latest: version('2.6.4'),
      plan: { steps: [{ ...step('Simple Voice Chat', '2.6.4'), projectId: '9eGKb6K1' }], manual: [], blockers: [], warnings: [], ready: true, fingerprint: 'fp-voice' },
      ports: [{ protocol: 'udp', port: 24454 }],
    }
    answer([
      ['/addons/checks', checks],
      ['/addons/curated', picked],
      ['/addons/project/modrinth/9eGKb6K1', details],
      ['/addons/search', library],
      ['/addons', none],
    ])
    reply([['/addons/install', () => ({ id: 'op-voice', kind: 'addon-install', status: 'running', phase: '', actor: 'siya', startedAt: '2026-09-26T00:00:00Z' })]])
    await render(server(), 'plugins', 'browse')
    const install = document.querySelector<HTMLButtonElement>('button[aria-label="Install Simple Voice Chat"]')
    if (!install) throw new Error('no Install on the voice chat card')
    await act(async () => install.click())
    for (let i = 0; i < 5; i++) await act(async () => {})
    const text = document.body.textContent ?? ''
    for (const line of ['Add proximity voice chat', 'Simple Voice Chat · Modrinth', 'UDP 24454', 'one more port', 'Voice travels on its own port', 'Playkeeper opens it on my-vps. Open UDP 24454 in your provider’s firewall too.', 'How to open a port', 'Friends who want to talk', 'Friends add the Simple Voice Chat mod to talk.', 'Copy the link for friends', 'Survival restarts for about 20 s.']) expect(text).toContain(line)
    expect(vi.mocked(client.post).mock.calls.some(([path]) => String(path).endsWith('/addons/install'))).toBe(false)
    await click('Install and open the port')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/addons/install', { source: 'modrinth', projectId: '9eGKb6K1', fingerprint: 'fp-voice', openPorts: true })
  })
})
