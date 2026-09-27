// @vitest-environment happy-dom
import { act, type ChangeEvent } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, FileContent, FileEntry, FileInfo, Files, MachineView, Me, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { serverTabsFor } from '@/components/app/server-tabs'
import { navigate } from '@/lib/router'
import { FilesPage } from '.'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  api: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
}))

// CodeMirror needs a real browser: a text box takes the editor's place, with the props the page gives it.
vi.mock('@/components/app/code-editor', () => ({
  CodeEditor: ({ value, readOnly, label, managed, onChange }: { value: string; readOnly: boolean; label: string; managed?: string[]; onChange: (text: string) => void }) => (
    <textarea aria-label={label} defaultValue={value} readOnly={readOnly} data-managed={managed?.join(',')} onChange={(e: ChangeEvent<HTMLTextAreaElement>) => onChange(e.target.value)} />
  ),
}))

const admin: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'servers.manage', 'files.view', 'files.edit']
const me = (can: Action[] = admin): Me => ({
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can },
})
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

function workspace(can: Action[] = admin): Workspace {
  return {
    me: me(can),
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

const at = '2026-09-26T21:04:12Z'
const entry = (name: string, type: FileEntry['type'] = 'file', size = 120): FileEntry => ({ name, type, size: type === 'file' ? size : 0, modifiedAt: at })
const worlds = ['world', 'world_nether', 'world_the_end']
const top = (running = true): Files => ({
  path: '',
  running,
  worlds,
  entries: [entry('server.properties', 'file', 1210), entry('world', 'folder'), entry('bukkit.yml'), entry('plugins', 'folder'), entry('server-icon.png', 'file', 5832), entry('evil.yml', 'link'), entry('world_nether', 'folder')],
})
const api = '/api/servers/abcdefghjk/files'
const list = (path: string) => `${api}?path=${encodeURIComponent(path)}`
const content = (path: string) => `${api}/content?path=${encodeURIComponent(path)}`

/** Answers GETs by their whole path; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => (path in routes ? Promise.resolve(routes[path]) : new Promise(() => {}))) as typeof client.get)
}

let root: Root | undefined

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => {})
}

/** Lets the editor's lazily loaded code arrive, as a first visit waits for it. */
async function loaded() {
  for (let i = 0; i < 100 && text().includes('Loading…'); i++) await act(async () => new Promise((done) => setTimeout(done, 10)))
  await settle()
}

async function render(opts: { path?: string; file?: boolean; ws?: Workspace; s?: ServerStatus } = {}): Promise<string> {
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () =>
    r.render(
      <WorkspaceContext.Provider value={opts.ws ?? workspace()}>
        <FilesPage server={opts.s ?? server()} path={opts.path ?? ''} file={opts.file ?? false} />
      </WorkspaceContext.Provider>,
    ),
  )
  await settle()
  if (opts.file) await loaded()
  return document.body.textContent ?? ''
}

const text = () => document.body.textContent ?? ''

function button(label: string): HTMLElement {
  const all = [...document.querySelectorAll<HTMLElement>('button, a')]
  const b = all.find((el) => el.textContent?.trim() === label || el.getAttribute('aria-label') === label)
  if (!b) throw new Error(`no button or link ${label}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await settle()
}

/** The row menu of an entry, opened. */
async function openMenu(name: string): Promise<HTMLElement[]> {
  await click(button(`More for ${name}`))
  return [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
}

async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

const rows = () => [...document.querySelectorAll('tbody tr')].map((tr) => tr.querySelector('td:nth-child(2)')?.textContent ?? '')
const happyDOM = (window as unknown as { happyDOM: { setViewport(v: { width: number; height: number }): void } }).happyDOM
const posts = (end: string) => vi.mocked(client.post).mock.calls.filter(([path]) => String(path).endsWith(end)).map(([, body]) => body)

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

beforeEach(() => {
  window.history.replaceState(null, '', '/servers/survival/files')
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  vi.mocked(client.post).mockReset()
  vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  vi.mocked(client.api).mockReset()
  vi.mocked(client.api).mockImplementation(() => Promise.resolve({}))
})

describe('the Files tab', () => {
  it('shows only to those who may see a server’s files', () => {
    const tabs = (can: Action[]) => serverTabsFor(me(can), server()).map((x) => x.tab)
    expect(tabs(admin)).toContain('files')
    expect(tabs(['view', 'servers.run', 'servers.console', 'players.manage'])).not.toContain('files')
  })

  it('lists a folder with its folders first, and marks the world in use while the game runs', async () => {
    answer({ [list('')]: top() })
    const shown = await render()
    expect(rows()).toEqual(['plugins', 'worldIn use', 'world_netherIn use', 'bukkit.yml', 'evil.ymlLink', 'server-icon.png', 'server.properties'])
    expect(shown).toContain('1.2 KB')
    expect(button('Upload')).toBeTruthy()
    expect(button('New')).toBeTruthy()
  })

  it('sorts by a column, folders first, and says which way pressing it again sorts', async () => {
    answer({ [list('')]: top() })
    await render()
    await click(button('Sort by Name, descending'))
    expect(rows()).toEqual(['world_netherIn use', 'worldIn use', 'plugins', 'server.properties', 'server-icon.png', 'evil.ymlLink', 'bukkit.yml'])
    await click(button('Sort by Size'))
    expect(rows()).toEqual(['world_netherIn use', 'worldIn use', 'plugins', 'server-icon.png', 'server.properties', 'bukkit.yml', 'evil.ymlLink'])
    expect(button('Sort by Size, ascending').closest('th')?.getAttribute('aria-sort')).toBe('descending')
    expect(button('Sort by Name').closest('th')?.getAttribute('aria-sort')).toBe('none')
  })

  it('keeps the world’s rows from changing while the game runs, and says why', async () => {
    answer({ [list('')]: top() })
    await render()
    const world = await openMenu('world')
    const worldItems = Object.fromEntries(world.map((el) => [el.textContent, el]))
    for (const label of ['Rename', 'Move', 'Delete']) {
      expect(worldItems[label]?.getAttribute('aria-disabled'), label).toBe('true')
      expect(worldItems[label]?.getAttribute('title')).toBe('Survival is running, so its world is read-only.')
    }
    expect(worldItems.Open?.getAttribute('aria-disabled')).not.toBe('true')
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    await settle()
    const file = await openMenu('bukkit.yml')
    expect(file.find((el) => el.textContent === 'Delete')?.getAttribute('aria-disabled')).not.toBe('true')
  })

  it('says on a phone why a world file can’t change while the game runs', async () => {
    happyDOM.setViewport({ width: 390, height: 844 })
    try {
      answer({ [list('world')]: { path: 'world', running: true, worlds, entries: [entry('level.dat', 'file', 5212)] } satisfies Files })
      await render({ path: 'world' })
      await click(button('More for level.dat'))
      const sheet = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')]
      for (const label of ['Rename', 'Move', 'Delete']) {
        const b = sheet.find((el) => el.getAttribute('aria-label') === label)
        expect(b?.disabled, label).toBe(true)
        expect(document.getElementById(b?.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Survival is running, so its world is read-only.')
      }
      expect(sheet.find((el) => el.textContent === 'Download')).toBeUndefined()
      expect(document.querySelector('[role="dialog"] a')?.textContent).toBe('Download')
    } finally {
      happyDOM.setViewport({ width: 1024, height: 768 })
    }
  })

  it('makes a stopped server’s world changeable', async () => {
    answer({ [list('')]: top(false) })
    await render({ s: server({ phase: 'stopped' }) })
    expect(rows()).toContain('world')
    const world = await openMenu('world')
    expect(world.find((el) => el.textContent === 'Delete')?.getAttribute('aria-disabled')).not.toBe('true')
  })

  it('says inside the world that it’s read-only while the game runs, with a way to stop it', async () => {
    answer({ [list('world')]: { path: 'world', running: true, worlds, entries: [entry('level.dat', 'file', 5212), entry('region', 'folder')] } satisfies Files })
    const shown = await render({ path: 'world' })
    // The notice says it once; the folders in the world aren't each marked in use.
    expect(rows()).toEqual(['region', 'level.dat'])
    expect(shown).toContain('Survival is running, so its world is read-only.')
    expect(shown).toContain('Stop it to change world files.')
    expect(button('Stop Survival')).toBeTruthy()
    expect(button('Upload').getAttribute('aria-disabled') ?? button('Upload').getAttribute('disabled')).not.toBeNull()
  })

  it('deletes only after asking, then reloads the folder', async () => {
    answer({ [list('')]: top() })
    await render()
    const items = await openMenu('bukkit.yml')
    await click(items.find((el) => el.textContent === 'Delete') as HTMLElement)
    expect(text()).toContain('Delete bukkit.yml?')
    expect(text()).toContain('This can’t be undone.')
    expect(posts('/files/delete')).toEqual([])
    const reads = vi.mocked(client.get).mock.calls.length
    const dialog = document.querySelector('[role="dialog"]') as HTMLElement
    await click([...dialog.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === 'Delete') as HTMLElement)
    expect(posts('/files/delete')).toEqual([{ paths: ['bukkit.yml'] }])
    expect(vi.mocked(client.get).mock.calls.length).toBeGreaterThan(reads)
  })

  it('makes a folder with a name the machine takes, and says what’s wrong with one it wouldn’t', async () => {
    answer({ [list('plugins')]: { path: 'plugins', running: true, worlds, entries: [entry('LuckPerms', 'folder'), entry('LuckPerms.jar')] } satisfies Files })
    await render({ path: 'plugins' })
    await click(button('New'))
    await click([...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent === 'New folder') as HTMLElement)
    const input = document.querySelector<HTMLInputElement>('#files-name') as HTMLInputElement
    const submit = button('Create folder')
    await type(input, 'old/configs')
    await click(submit)
    expect(text()).toContain('A name can’t contain a slash.')
    await type(input, 'LuckPerms')
    await click(submit)
    expect(text()).toContain('Something here already has that name.')
    expect(posts('/files/folder')).toEqual([])
    await type(input, 'backups')
    await click(submit)
    expect(posts('/files/folder')).toEqual([{ path: 'plugins/backups' }])
  })

  it('renames in place', async () => {
    answer({ [list('')]: top(false) })
    await render({ s: server({ phase: 'stopped' }) })
    const items = await openMenu('bukkit.yml')
    await click(items.find((el) => el.textContent === 'Rename') as HTMLElement)
    const input = document.querySelector<HTMLInputElement>('#files-name') as HTMLInputElement
    expect(input.value).toBe('bukkit.yml')
    await type(input, 'bukkit-old.yml')
    await click(button('Rename'))
    expect(posts('/files/move')).toEqual([{ items: [{ from: 'bukkit.yml', to: 'bukkit-old.yml' }] }])
  })

  it('lets someone who may only look open and download, and change nothing', async () => {
    answer({ [list('')]: top() })
    await render({ ws: workspace(['view', 'servers.run', 'files.view']) })
    expect(() => button('Upload')).toThrow()
    expect(() => button('New')).toThrow()
    const items = await openMenu('bukkit.yml')
    expect(items.map((el) => el.textContent)).toEqual(['Open', 'Download'])
    expect(items[1]?.getAttribute('href')).toBe(`${api}/download?path=bukkit.yml`)
  })

  it('asks before an upload replaces what has its name', async () => {
    answer({ [list('')]: top(false) })
    await render({ s: server({ phase: 'stopped' }) })
    const input = document.querySelector<HTMLInputElement>('input[type="file"][aria-label="Files to upload"]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [new File(['motd=x\n'], 'server.properties'), new File(['x: 1\n'], 'extra.yml')], configurable: true })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    await settle()
    expect(text()).toContain('server.properties is already here')
    expect(vi.mocked(client.post)).not.toHaveBeenCalled()
    await click(button('Skip'))
    expect(vi.mocked(client.post).mock.calls[0]).toEqual([`${api}/uploads`, { folder: '' }])
  })
})

const properties: FileContent = {
  path: 'server.properties',
  size: 42,
  modifiedAt: at,
  version: 'a'.repeat(64),
  text: 'motd=Survival with friends\nmax-players=10\n',
  running: true,
  managed: ['max-players', 'motd'],
}

describe('the editor', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/servers/survival/file/server.properties')
  })

  it('opens a text file, says which lines come from Settings, and saves over the version it opened', async () => {
    answer({ [content('server.properties')]: properties })
    const saved: FileInfo = { path: 'server.properties', size: 43, modifiedAt: at, version: 'b'.repeat(64) }
    vi.mocked(client.api).mockResolvedValue(saved)
    const shown = await render({ path: 'server.properties', file: true })
    expect(shown).toContain('Marked lines are set from Settings each time Survival starts.')
    const box = document.querySelector<HTMLTextAreaElement>('textarea') as HTMLTextAreaElement
    expect(box.value).toBe(properties.text)
    expect(box.dataset.managed).toBe('max-players,motd')
    expect(box.readOnly).toBe(false)
    await type(box, 'motd=Survival with friends\nmax-players=12\n')
    expect(text()).toContain('Unsaved changes')
    await click(button('Save'))
    const [method, path, body, raw] = vi.mocked(client.api).mock.calls[0] ?? []
    expect([method, path, body]).toEqual(['PUT', `${content('server.properties')}&expect=${'a'.repeat(64)}`, undefined])
    expect(await (raw as Blob).text()).toBe('motd=Survival with friends\nmax-players=12\n')
    expect(text()).toContain('Saved. Restart Survival to use the change.')
  })

  it('says when the file changed since it was opened, and saves over it only when asked', async () => {
    answer({ [content('server.properties')]: properties })
    vi.mocked(client.api).mockRejectedValueOnce(new client.ApiError(409, { error: '"server.properties" changed since you opened it.', code: 'file_changed', params: { path: 'server.properties', gone: false } }))
    await render({ path: 'server.properties', file: true })
    await type(document.querySelector('textarea') as HTMLTextAreaElement, 'motd=Mine\n')
    await click(button('Save'))
    expect(text()).toContain('server.properties changed since you opened it')
    vi.mocked(client.api).mockResolvedValueOnce({ path: 'server.properties', size: 10, modifiedAt: at, version: 'c'.repeat(64) } satisfies FileInfo)
    await click(button('Save mine over it'))
    expect(vi.mocked(client.api).mock.calls[1]?.[1]).toBe(content('server.properties'))
  })

  it('opens a world file read-only while the game runs', async () => {
    const level: FileContent = { path: 'world/paper-world.yml', size: 30, modifiedAt: at, version: 'd'.repeat(64), text: '_version: 31\n', running: true, readOnly: 'world_in_use' }
    window.history.replaceState(null, '', '/servers/survival/file/world/paper-world.yml')
    answer({ [content('world/paper-world.yml')]: level })
    const shown = await render({ path: 'world/paper-world.yml', file: true })
    expect(shown).toContain('Survival is running, so its world is read-only.')
    expect(document.querySelector<HTMLTextAreaElement>('textarea')?.readOnly).toBe(true)
    expect(shown).toContain('Read-only')
  })

  it('offers a file that isn’t text as a download', async () => {
    window.history.replaceState(null, '', '/servers/survival/file/world/level.dat')
    answer({ [content('world/level.dat')]: { path: 'world/level.dat', size: 5212, modifiedAt: at, version: 'e'.repeat(64), text: '', binary: true, running: false } satisfies FileContent })
    const shown = await render({ path: 'world/level.dat', file: true, s: server({ phase: 'stopped' }) })
    expect(shown).toContain('This file isn’t text')
    expect(document.querySelector('textarea')).toBeNull()
    expect(button('Download').getAttribute('href')).toBe(`${api}/download?path=world%2Flevel.dat`)
  })

  it('asks before unsaved changes are left behind', async () => {
    answer({ [content('server.properties')]: properties })
    await render({ path: 'server.properties', file: true })
    await type(document.querySelector('textarea') as HTMLTextAreaElement, 'motd=Changed\n')
    await act(async () => navigate({ name: 'server', slug: 'survival', tab: 'overview' }))
    await settle()
    expect(text()).toContain('Discard your changes to server.properties?')
    expect(window.location.pathname).toBe('/servers/survival/file/server.properties')
    await click(button('Keep editing'))
    expect(text()).not.toContain('Discard your changes')
    await act(async () => navigate({ name: 'server', slug: 'survival', tab: 'overview' }))
    await settle()
    await click(button('Discard changes'))
    expect(window.location.pathname).toBe('/servers/survival')
  })
})
