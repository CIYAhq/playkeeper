import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type * as client from '@/api/client'
import type { Activity, Addons, AuditEntry, FileContent, FileInfo, Files, MachineView, Operation, ServerStatus } from '@/api/types'
import { answer, resetDemo } from './engine'
import { demoToast } from './toast'
import { uploadFiles } from './upload'

vi.mock('./toast', () => ({ demoToast: vi.fn() }))
// The demo build gives lib/upload the demo's client; here the real client
// asks the engine instead, as in samples.test.ts.
vi.mock('@/api/client', async (real) => {
  const actual = await real<typeof client>()
  const engine = () => import('./engine')
  return { ...actual, get: async (path: string) => (await engine()).answer('GET', path), post: async (path: string, body: unknown = {}) => (await engine()).answer('POST', path, body) }
})

async function ask<T>(method: string, path: string, body?: unknown, raw?: Blob): Promise<T> {
  const reply = answer(method, path, body, raw).then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  )
  await vi.advanceTimersByTimeAsync(200)
  const settled = await reply
  if ('error' in settled) throw settled.error
  return settled.value as T
}

async function server(slug: string): Promise<ServerStatus> {
  const found = (await ask<ServerStatus[]>('GET', '/api/servers')).find((s) => s.slug === slug)
  if (!found) throw new Error(`no ${slug} in the sample data`)
  return found
}

const files = (id: string, rest = '') => `/api/servers/${id}/files${rest}`
const q = encodeURIComponent
const list = (id: string, path = '') => ask<Files>('GET', files(id, `?path=${q(path)}`))
const open = (id: string, path: string) => ask<FileContent>('GET', files(id, `/content?path=${q(path)}`))
const save = (id: string, path: string, text: string, expect = '') => ask<FileInfo>('PUT', files(id, `/content?path=${q(path)}${expect ? `&expect=${q(expect)}` : ''}`), undefined, new Blob([text], { type: 'text/plain' }))
const names = (f: Files) => f.entries.map((e) => e.name)
const activity = async (id: string) => ask<Activity[]>('GET', `/api/servers/${id}/activity?limit=5`)
const audits = () => ask<AuditEntry[]>('GET', '/api/audit')

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-25T12:10:00Z'))
  resetDemo()
  vi.mocked(demoToast).mockClear()
})

afterEach(() => {
  vi.useRealTimers()
})

it('shows each server’s folder from what its other tabs show', async () => {
  const survival = await server('survival')
  const root = await list(survival.id)
  expect(root).toMatchObject({ path: '', running: true, worlds: ['world', 'world_nether', 'world_the_end'] })
  expect(names(root)).toEqual(expect.arrayContaining(['server.properties', 'eula.txt', 'whitelist.json', 'ops.json', 'bukkit.yml', 'plugins', 'world', 'world_nether', 'world_the_end', 'logs']))
  expect(root.entries.find((e) => e.name === 'plugins')?.type).toBe('folder')

  const addons = await ask<Addons>('GET', `/api/servers/${survival.id}/addons`)
  const jars = names(await list(survival.id, 'plugins')).filter((n) => n.endsWith('.jar'))
  expect(jars.sort()).toEqual(addons.files.map((f) => f.fileName).sort())

  const whitelist = JSON.parse((await open(survival.id, 'whitelist.json')).text) as { name: string }[]
  expect(whitelist.map((p) => p.name)).toEqual(['JunoFox', 'tobi2009', 'mara_k', 'PixelPia', 'Brickbert', 'Kestrel_7'])
  const props = await open(survival.id, 'server.properties')
  expect(props.text).toContain('motd=Survival with friends')
  expect(props.managed).toEqual(expect.arrayContaining(['motd', 'max-players', 'resource-pack']))
  expect((await open(survival.id, 'server-icon.png')).binary).toBe(true)
  await expect(open(survival.id, 'world/region/r.0.0.mca')).rejects.toMatchObject({ status: 409, code: 'too_large' })

  const cobblemon = await server('cobblemon')
  expect(names(await list(cobblemon.id, 'mods')).sort()).toEqual(['Cobblemon-fabric-1.7.1.jar', 'fabric-api-0.140.2.jar', 'lithium-fabric-0.18.1.jar'])
  const [report] = (await list(cobblemon.id, 'crash-reports')).entries
  expect((await open(cobblemon.id, `crash-reports/${report?.name ?? ''}`)).text).toContain('java.lang.OutOfMemoryError: Java heap space')
})

it('keeps a running server’s world read-only, and lets it change once the server stops', async () => {
  const survival = await server('survival')
  const world = await open(survival.id, 'world/paper-world.yml')
  expect(world.readOnly).toBe('world_in_use')
  await expect(save(survival.id, 'world/paper-world.yml', world.text, world.version)).rejects.toMatchObject({ status: 409, code: 'world_in_use' })
  await expect(ask('POST', files(survival.id, '/delete'), { paths: ['world/level.dat'] })).rejects.toMatchObject({ code: 'world_in_use' })
  await expect(ask('POST', files(survival.id, '/move'), { items: [{ from: 'bukkit.yml', to: 'world_nether/bukkit.yml' }] })).rejects.toMatchObject({ code: 'world_in_use' })
  // Other files change while it runs, and apply when it restarts.
  expect((await open(survival.id, 'bukkit.yml')).readOnly).toBeUndefined()

  await ask<Operation>('POST', `/api/servers/${survival.id}/stop`)
  await expect(save(survival.id, 'bukkit.yml', 'settings: {}\n')).rejects.toMatchObject({ status: 409, code: 'busy' })
  await vi.advanceTimersByTimeAsync(10_000)
  const stopped = await open(survival.id, 'world/paper-world.yml')
  expect(stopped).toMatchObject({ running: false })
  expect(stopped.readOnly).toBeUndefined()
  await expect(save(survival.id, 'world/paper-world.yml', `${stopped.text}# checked\n`, stopped.version)).resolves.toMatchObject({ path: 'world/paper-world.yml' })

  const creative = await server('creative')
  expect((await list(creative.id)).running).toBe(false)
})

it('saves over the version it opened, says when the file changed meanwhile, and makes new files', async () => {
  const { id } = await server('survival')
  const first = await open(id, 'help.yml')
  const saved = await save(id, 'help.yml', `${first.text}# one\n`, first.version)
  expect(saved.version).not.toBe(first.version)
  expect((await open(id, 'help.yml')).text).toContain('# one')
  await expect(save(id, 'help.yml', 'stale\n', first.version)).rejects.toMatchObject({ code: 'file_changed', params: { gone: false } })
  await expect(save(id, 'help.yml', 'mine anyway\n')).resolves.toMatchObject({ size: 12 })

  await expect(save(id, 'notes.txt', 'Build night on Friday\n', 'new')).resolves.toMatchObject({ path: 'notes.txt' })
  await expect(save(id, 'notes.txt', 'again\n', 'new')).rejects.toMatchObject({ code: 'exists' })
  await ask('POST', files(id, '/delete'), { paths: ['notes.txt'] })
  await expect(save(id, 'notes.txt', 'gone?\n', saved.version)).rejects.toMatchObject({ code: 'file_changed', params: { gone: true } })

  const kinds = (await activity(id)).map((a) => [a.kind, a.detail])
  expect(kinds.slice(0, 4)).toEqual([
    ['file_deleted', 'notes.txt'],
    ['file_created', 'notes.txt'],
    ['file_saved', 'help.yml'],
    ['file_saved', 'help.yml'],
  ])
  expect((await audits()).slice(0, 2).map((e) => [e.action, e.detail])).toEqual([
    ['files.deleted', 'notes.txt'],
    ['files.created', 'notes.txt'],
  ])
})

it('makes folders, renames, moves and deletes, and the Add-ons tab follows its jars', async () => {
  const { id } = await server('survival')
  await ask('POST', files(id, '/folder'), { path: 'old/plugins' })
  expect(names(await list(id, 'old'))).toEqual(['plugins'])
  await expect(ask('POST', files(id, '/folder'), { path: 'old' })).rejects.toMatchObject({ status: 409, code: 'exists' })

  await ask('POST', files(id, '/move'), { items: [{ from: 'help.yml', to: 'help-old.yml' }] })
  expect((await activity(id))[0]).toMatchObject({ kind: 'file_renamed', detail: 'help.yml → help-old.yml' })
  await ask('POST', files(id, '/move'), { items: [{ from: 'plugins/FriendsWelcome.jar', to: 'old/plugins/FriendsWelcome.jar' }] })
  expect((await activity(id))[0]).toMatchObject({ kind: 'file_moved', detail: 'plugins/FriendsWelcome.jar → old/plugins/FriendsWelcome.jar' })
  await expect(ask('POST', files(id, '/move'), { items: [{ from: 'old', to: 'old/plugins/old' }] })).rejects.toMatchObject({ code: 'into_itself' })
  await expect(ask('POST', files(id, '/move'), { items: [{ from: 'eula.txt', to: 'ops.json' }] })).rejects.toMatchObject({ code: 'exists' })

  await ask('POST', files(id, '/delete'), { paths: ['plugins/CoreProtect-23.1.jar', 'plugins/CoreProtect', 'not-here.txt'] })
  expect((await activity(id))[0]).toMatchObject({ kind: 'file_deleted', detail: 'plugins/CoreProtect-23.1.jar, plugins/CoreProtect, not-here.txt' })
  expect(names(await list(id, 'plugins'))).not.toContain('CoreProtect')
  const left = (await ask<Addons>('GET', `/api/servers/${id}/addons`)).files.map((f) => f.fileName)
  expect(left).not.toContain('FriendsWelcome.jar')
  expect(left.some((n) => n.startsWith('CoreProtect'))).toBe(false)
  expect(left.sort()).toEqual(['Chunky-Bukkit-1.5.3.jar', 'LuckPerms-Bukkit-5.5.10.jar', 'ViaVersion-5.5.1.jar', 'bluemap-5.13-paper.jar'])
})

it('refuses any path outside the server’s folder', async () => {
  const { id } = await server('survival')
  for (const path of ['..', '../other', '/etc/passwd', 'plugins/../..', 'plugins//x', './eula.txt', 'plugins/.', 'a\u0000b']) {
    await expect(list(id, path), path).rejects.toMatchObject({ status: 400, code: 'invalid_request' })
    await expect(open(id, path), path).rejects.toMatchObject({ status: 400 })
  }
  await expect(save(id, '../server.properties', 'x=1\n')).rejects.toMatchObject({ status: 400 })
  await expect(ask('POST', files(id, '/move'), { items: [{ from: 'eula.txt', to: '../eula.txt' }] })).rejects.toMatchObject({ status: 400 })
  await expect(ask('POST', files(id, '/delete'), { paths: ['../'] })).rejects.toMatchObject({ status: 400 })
  await expect(ask('POST', files(id, '/folder'), { path: 'bad\u0007name' })).rejects.toMatchObject({ status: 400 })
  await expect(ask('GET', files(id, `/download?path=${q('../..')}`))).rejects.toMatchObject({ status: 400 })
  expect(names(await list(id))).toContain('eula.txt')
})

it('names the server’s folder as the agent does when several files go up to its top', async () => {
  const { id } = await server('survival')
  const upload = uploadFiles({ base: files(id, '/uploads'), folder: '', files: [new File(['a\n'], 'notes-a.txt'), new File(['b\n'], 'notes-b.txt')], signal: new AbortController().signal })
  await vi.advanceTimersByTimeAsync(10_000)
  await upload
  expect((await activity(id))[0]).toMatchObject({ kind: 'file_uploaded', count: 2, detail: '' })
})

it('uploads files without sending a byte: a small text file opens in the editor, and a name in use needs replacing', async () => {
  const { id } = await server('survival')
  const base = files(id, '/uploads')
  const signal = new AbortController().signal
  const upload = uploadFiles({ base, folder: 'plugins/FriendsWelcome', files: [new File(['greeting: hi\n'], 'messages.yml'), new File(['farewell: bye\n'], 'goodbye.yml')], signal })
  await vi.advanceTimersByTimeAsync(10_000)
  const done = await upload
  expect(done.files.map((f) => [f.name, f.placed])).toEqual([
    ['messages.yml', true],
    ['goodbye.yml', true],
  ])
  expect((await open(id, 'plugins/FriendsWelcome/messages.yml')).text).toBe('greeting: hi\n')
  expect((await activity(id))[0]).toMatchObject({ kind: 'file_uploaded', count: 2, detail: 'plugins/FriendsWelcome' })

  const again = uploadFiles({ base, folder: 'plugins/FriendsWelcome', files: [new File(['greeting: hello\n'], 'messages.yml')], signal })
  const refused = again.catch((e: unknown) => e)
  await vi.advanceTimersByTimeAsync(10_000)
  expect(await refused).toMatchObject({ status: 409, code: 'exists' })
  const replaced = uploadFiles({ base, folder: 'plugins/FriendsWelcome', files: [new File(['greeting: hello\n'], 'messages.yml')], replace: true, signal })
  await vi.advanceTimersByTimeAsync(10_000)
  await replaced
  expect((await open(id, 'plugins/FriendsWelcome/messages.yml')).text).toBe('greeting: hello\n')

  const world = uploadFiles({ base, folder: 'world/datapacks', files: [new File(['{}'], 'pack.zip')], signal })
  const inUse = world.catch((e: unknown) => e)
  await vi.advanceTimersByTimeAsync(10_000)
  expect(await inUse).toMatchObject({ code: 'world_in_use' })
})

it('answers a download with the demo’s toast and an audit row', async () => {
  const { id } = await server('survival')
  await ask('GET', files(id, `/download?path=${q('plugins/Chunky')}&path=${q('plugins/LuckPerms')}`))
  expect(demoToast).toHaveBeenCalledWith('fileDownload')
  expect((await audits())[0]).toMatchObject({ action: 'files.downloaded', target: 'plugins', detail: 'plugins/Chunky, plugins/LuckPerms' })
  await expect(ask('GET', files(id, `/download?path=eula.txt&path=${q('plugins/Chunky')}`))).rejects.toMatchObject({ status: 400 })
  await expect(ask('GET', files(id, '/download?path=missing.txt'))).rejects.toMatchObject({ status: 404 })
})

it('gives a server made in the demo a folder of its own', async () => {
  const [machine] = await ask<MachineView[]>('GET', '/api/machines')
  const op = await ask<Operation>('POST', `/api/machines/${machine?.id ?? ''}/servers`, { name: 'Weekend', memoryMB: 2048 })
  await vi.advanceTimersByTimeAsync(20_000)
  const id = op.serverId ?? ''
  expect(names(await list(id))).toEqual(expect.arrayContaining(['server.properties', 'plugins', 'world']))
  expect(names(await list(id, 'plugins'))).toEqual([])
})
