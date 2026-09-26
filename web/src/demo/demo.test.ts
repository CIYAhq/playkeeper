import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Address, AddonBrowse, AddonChecks, AddonDetails, AddonRemovePreview, Addons, AddonSources, Catalog, CatalogEntry, CuratedAddons, DataPacks, LogsResponse, MemoryAdvice, PackShare, Pregen, ResourcePack, Running, ServerStatus, SoftwareBuilds, TwoFactorStatus } from '@/api/types'
import { addonIconOf, faceOf, library, samplePlayers } from './data'
import { hasBuilds, pinBuild } from '@/lib/software'
import { upgradeTargets } from '@/lib/versions'
import { answer, resetDemo } from './engine'
import { faceCount } from './faces'
import { iconCount, iconSvg } from './icons'
import { demoMarker } from './marker'
import { demoToast } from './toast'
import { noDemo } from './vite'

vi.mock('./toast', () => ({ demoToast: vi.fn() }))

const src = fileURLToPath(new URL('..', import.meta.url))
const demoDir = join(src, 'demo')

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (path === demoDir) return []
    if (statSync(path).isDirectory()) return files(path)
    return /\.tsx?$/.test(path) ? [path] : []
  })
}

it('keeps the live demo out of the dashboard: nothing outside src/demo imports it', () => {
  const specifiers = /\bfrom\s+'([^']+)'|\bimport\s*\(\s*'([^']+)'\s*\)|\bimport\s+'([^']+)'/g
  const found = files(src).flatMap((path) =>
    [...readFileSync(path, 'utf8').matchAll(specifiers)].flatMap((m) => {
      const spec = m[1] ?? m[2] ?? m[3] ?? ''
      const target = spec.startsWith('@/') ? join(src, spec.slice(2)) : spec.startsWith('.') ? resolve(dirname(path), spec) : ''
      return target === demoDir || target.startsWith(demoDir + sep) ? [`${relative(src, path)} imports ${spec}`] : []
    }),
  )
  expect(found).toEqual([])
})

it('fails any other build that picks up the demo, by module or by its marker', () => {
  type Generate = (this: { error: (message: string) => never }, options: unknown, bundle: Record<string, unknown>) => void
  const generate = noDemo().generateBundle as unknown as Generate
  const context = {
    error: (message: string): never => {
      throw new Error(message)
    },
  }
  const bundle = (moduleIds: string[], code = '') => ({ 'assets/index.js': { type: 'chunk', fileName: 'assets/index.js', code, moduleIds } })
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx')], 'console.log(1)'))).not.toThrow()
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx'), join(demoDir, 'data.ts')]))).toThrow(/contains the live demo/)
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx')], `const k = "${demoMarker}"`))).toThrow(/contains the live demo/)
})

it('gives every sample player a face of their own, and anyone else one of the same faces', () => {
  expect(new Set(samplePlayers.map(faceOf)).size).toBe(samplePlayers.length)
  for (const name of ['Steve', 'alex_2', 'x', 'JunoFox2']) {
    expect(faceOf(name)).toBeGreaterThanOrEqual(0)
    expect(faceOf(name)).toBeLessThan(faceCount)
  }
})

it('draws every plugin in the library an icon of its own', () => {
  const icons = library.map((a) => a.icon)
  expect(icons.every((n) => n >= 0 && n < iconCount)).toBe(true)
  expect(new Set(icons.map(iconSvg)).size).toBe(library.length)
  expect(addonIconOf('https://cdn.modrinth.com/data/elsewhere/icon.png')).toBeUndefined()
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
  const servers = await ask<ServerStatus[]>('GET', '/api/servers')
  const found = servers.find((s) => s.slug === slug)
  if (!found) throw new Error(`no ${slug} in the sample data`)
  return found
}

const survival = () => server('survival')

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-25T12:10:00Z'))
  resetDemo()
  vi.mocked(demoToast).mockClear()
})

afterEach(() => {
  vi.useRealTimers()
})

it('fails soft where it has nothing to show or change', async () => {
  await expect(ask('GET', '/api/nothing-here')).rejects.toMatchObject({ status: 404, code: 'not_found' })
  await expect(ask('POST', '/api/nothing-here', {})).rejects.toMatchObject({ status: 400, code: 'demo' })
  const { id } = await survival()
  await expect(ask('POST', `/api/servers/${id}/world`, undefined, new Blob(['x']))).rejects.toMatchObject({ status: 400, code: 'demo' })
})

it('has one machine, with no events and no joining, for the machines pages', async () => {
  const machines = await ask<{ id: string; kind: string }[]>('GET', '/api/machines')
  expect(machines.map((m) => m.kind)).toEqual(['local'])
  await expect(ask('GET', `/api/machines/${machines[0]?.id}/events`)).resolves.toEqual([])
  await expect(ask('POST', '/api/join-codes', { name: '', dial: 'name' })).rejects.toMatchObject({ status: 400, code: 'demo' })
})

it('plays a restart out, back online, and ends it with the demo toast', async () => {
  const before = await survival()
  expect(before.phase).toBe('online')
  await ask('POST', `/api/servers/${before.id}/restart`)
  expect((await survival()).phase).not.toBe('online')
  await expect(ask('POST', `/api/servers/${before.id}/stop`)).rejects.toMatchObject({ status: 409, code: 'busy' })
  expect(demoToast).not.toHaveBeenCalled()

  await vi.advanceTimersByTimeAsync(20_000)
  const after = await survival()
  expect(after.phase).toBe('online')
  expect(after.operation).toBeUndefined()
  expect(demoToast).toHaveBeenCalledWith('restart')
})

it('keeps console lines arriving', async () => {
  const { id } = await survival()
  const first = await ask<LogsResponse>('GET', `/api/servers/${id}/logs`)
  expect(first.lines.length).toBeGreaterThan(0)
  await vi.advanceTimersByTimeAsync(30_000)
  const more = await ask<LogsResponse>('GET', `/api/servers/${id}/logs?after=${first.next}`)
  expect(more.lines.length).toBeGreaterThan(0)
  expect(more.lines.every((l) => l.seq > first.next)).toBe(true)
})

it('starts over on the hour', async () => {
  const { id } = await survival()
  await ask('POST', `/api/servers/${id}/stop`)
  await vi.advanceTimersByTimeAsync(20_000)
  expect((await survival()).phase).toBe('stopped')

  vi.setSystemTime(new Date('2026-09-25T13:00:05Z'))
  expect((await survival()).phase).toBe('online')
})

it('lists Survival’s plugins as a small server has them: from both libraries, one behind, one added by hand', async () => {
  const { id } = await survival()
  const list = await ask<Addons>('GET', `/api/servers/${id}/addons`)
  expect(list.target).toMatchObject({ kind: 'plugin', folder: 'plugins', minecraftVersion: '26.1.2' })
  const managed = list.files.filter((f) => f.status === 'managed')
  expect(managed.length).toBeGreaterThanOrEqual(4)
  expect(new Set(managed.map((f) => f.addon?.source))).toEqual(new Set(['modrinth', 'hangar']))
  expect(managed.every((f) => addonIconOf(f.addon?.iconUrl ?? '') !== undefined)).toBe(true)
  expect(list.files.filter((f) => f.status === 'unknown').map((f) => f.fileName)).toEqual(['FriendsWelcome.jar'])

  const checks = await ask<AddonChecks>('GET', `/api/servers/${id}/addons/checks`)
  expect(checks.updates.filter((u) => u.available).map((u) => [u.projectId, u.latest?.versionNumber])).toEqual([['Vebnzrzj', '5.5.11']])
  const luckPerms = await ask<AddonDetails>('GET', `/api/servers/${id}/addons/project/modrinth/Vebnzrzj`)
  expect(luckPerms).toMatchObject({ installed: { versionNumber: '5.5.10' }, latest: { versionNumber: '5.5.11' }, updateAvailable: true })
  const removal = await ask<AddonRemovePreview>('GET', `/api/servers/${id}/addons/project/modrinth/Vebnzrzj/removal`)
  expect(removal).toMatchObject({ addon: { name: 'LuckPerms' }, neededBy: [], configFolder: 'plugins/LuckPerms' })
  const geyser = await ask<AddonDetails>('GET', `/api/servers/${id}/addons/project/hangar/Geyser`)
  expect(geyser.installed).toBeUndefined()
  expect(geyser.plan).toMatchObject({ ready: true, steps: [{ action: 'install', name: 'Geyser' }] })
  await expect(ask('GET', `/api/servers/${id}/addons/project/modrinth/elsewhere`)).rejects.toMatchObject({ status: 404 })
})

it('searches the library by words, category and order, and marks what the server has', async () => {
  const { id } = await survival()
  const search = (query = '') => ask<AddonBrowse>('GET', `/api/servers/${id}/addons/search${query}`)
  const all = await search()
  expect(all.cards).toHaveLength(library.filter((a) => !a.mod).length)
  const downloads = all.cards.map((c) => c.downloads)
  expect(downloads).toEqual([...downloads].sort((a, b) => b - a))
  expect(all.cards.find((c) => c.slug === 'chunky')?.installed).toBe(true)
  expect(all.cards.find((c) => c.slug === 'Geyser')?.installed).toBe(false)
  expect((await search('?q=perms')).cards.map((c) => c.name)).toEqual(['LuckPerms'])
  const chat = (await search('?category=chat')).cards
  expect(chat.length).toBeGreaterThan(0)
  expect(chat.every((c) => c.categories.includes('chat'))).toBe(true)
  expect((await search('?sort=updated')).cards[0]?.name).toBe('LuckPerms')
  expect((await search('?page=1')).cards).toEqual([])
})

it('pre-generates Survival’s map while it runs, and finishes it', async () => {
  const { id } = await survival()
  const pregen = () => ask<Pregen>('GET', `/api/servers/${id}/pregen`)
  const first = await pregen()
  expect(first).toMatchObject({ state: 'running', preset: 'medium', radius: 2500, installed: true, total: 99_225 })
  expect(first.percent).toBeGreaterThan(0)
  expect(first.percent).toBeLessThan(100)
  expect(first.presets.map((p) => p.id)).toEqual(['small', 'medium', 'large', 'huge'])

  await vi.advanceTimersByTimeAsync(5 * 60_000)
  const later = await pregen()
  expect(later.chunks).toBeGreaterThan(first.chunks)
  expect(later.etaSeconds).toBeLessThan(first.etaSeconds)

  await vi.advanceTimersByTimeAsync(40 * 60_000)
  const done = await pregen()
  expect(done).toMatchObject({ state: 'finished', percent: 100, chunks: 99_225 })
  expect(done.diskBytes).toBeGreaterThan(0)
})

it('pauses the pre-generation where it was while its server is stopped', async () => {
  const { id } = await survival()
  await ask('POST', `/api/servers/${id}/stop`)
  await vi.advanceTimersByTimeAsync(20_000)
  const stopped = await ask<Pregen>('GET', `/api/servers/${id}/pregen`)
  expect(stopped).toMatchObject({ state: 'paused', pausedBy: 'server' })
  await vi.advanceTimersByTimeAsync(60_000)
  expect((await ask<Pregen>('GET', `/api/servers/${id}/pregen`)).chunks).toBe(stopped.chunks)
})

it('offers a resource pack from the address the demo is open at, and three data packs', async () => {
  const { id } = await survival()
  const rp = await ask<ResourcePack>('GET', `/api/servers/${id}/resourcepack`)
  expect(rp.offer?.fileName).toBe('Cosy_Blocks_32x.zip')
  expect(new URL(rp.offer?.url ?? '').hostname).toBe('demo.playkeeper.io')
  const dp = await ask<DataPacks>('GET', `/api/servers/${id}/datapacks`)
  expect(dp.live).toBe(true)
  expect(dp.packs.map((p) => p.enabled)).toEqual([true, true, false])
})

it('gives Creative WorldEdit, and no pre-generation or packs yet', async () => {
  const { id } = await server('creative')
  const list = await ask<Addons>('GET', `/api/servers/${id}/addons`)
  expect(list.files.map((f) => f.addon?.name)).toEqual(['WorldEdit'])
  expect(await ask<Pregen>('GET', `/api/servers/${id}/pregen`)).toMatchObject({ state: 'idle', installed: false })
  expect((await ask<ResourcePack>('GET', `/api/servers/${id}/resourcepack`)).offer).toBeUndefined()
  expect(await ask<DataPacks>('GET', `/api/servers/${id}/datapacks`)).toEqual({ packs: [], live: false })
})

it('fails soft when asked to change plugins, the pre-generation or packs', async () => {
  const { id } = await survival()
  const writes: [string, string][] = [
    ['POST', '/addons/install'],
    ['POST', '/addons/update/plan'],
    ['POST', '/addons/update'],
    ['POST', '/addons/remove'],
    ['POST', '/addons/forget'],
    ['POST', '/pregen/pause'],
    ['POST', '/pregen/cancel'],
    ['POST', '/datapacks/More_Mob_Heads.zip/enable'],
    ['POST', '/resourcepack/settings'],
    ['DELETE', '/resourcepack'],
  ]
  for (const [method, path] of writes) await expect(ask(method, `/api/servers/${id}${path}`, {})).rejects.toMatchObject({ status: 400, code: 'demo' })
  await expect(ask('POST', `/api/servers/${id}/datapacks?name=pack.zip`, undefined, new Blob(['x']))).rejects.toMatchObject({ status: 400, code: 'demo' })
  expect((await ask<Addons>('GET', `/api/servers/${id}/addons`)).files).toHaveLength(6)
})

it('gives the machine a free name nobody can claim, with an address for each server', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  const address = await ask<Address>('GET', `/api/machines/${m?.id}/address`)
  expect(address).toMatchObject({ kind: 'playkeeper', host: 'demo.playkeeper.io', free: { name: 'demo', state: 'active', dns: 'ok' }, certificate: { names: ['demo.playkeeper.io'] } })
  const servers = await ask<ServerStatus[]>('GET', '/api/servers')
  expect(address.servers?.map((s) => s.address)).toEqual(servers.map((s) => s.joinAddress))
  expect(servers.map((s) => s.joinAddress)).toEqual(['survival.demo.playkeeper.io', 'creative.demo.playkeeper.io', 'cobblemon.demo.playkeeper.io'])
  await expect(ask('POST', `/api/machines/${m?.id}/address/claim`, { name: 'alex' })).rejects.toMatchObject({ status: 400, code: 'demo' })
  expect(await ask<TwoFactorStatus>('GET', '/api/auth/2fa')).toMatchObject({ state: 'off' })
  await expect(ask('POST', '/api/auth/2fa/setup', { password: 'x' })).rejects.toMatchObject({ status: 400, code: 'demo' })
  expect(await ask<AddonSources>('GET', `/api/machines/${m?.id}/addon-sources`)).toEqual({ curseforge: { key: 'none' } })
})

it('says how each server runs and what memory it needs', async () => {
  const survivalId = (await survival()).id
  expect(await ask<Running>('GET', `/api/servers/${survivalId}/running`)).toMatchObject({ status: 'smooth', params: { tps: 20 } })
  expect((await ask<Running>('GET', `/api/servers/${(await server('creative')).id}/running`)).status).toBe('unknown')
  const keep = await ask<MemoryAdvice>('GET', `/api/servers/${survivalId}/memory`)
  expect(keep).toMatchObject({ verdict: 'keep', budgetMB: 4096, recommendedMB: 4096 })
  expect(keep.days).toHaveLength(14)
  expect(keep.options.map((o) => [o.memoryMB, o.fit])).toEqual([
    [2048, 'too_tight'],
    [3072, 'little_room'],
    [4096, 'room_to_grow'],
    [6144, 'more_than_needed'],
    [8192, 'more_than_needed'],
  ])
  expect((await ask<MemoryAdvice>('GET', `/api/servers/${(await server('creative')).id}/memory`)).verdict).toBe('lower')
  expect(await ask<MemoryAdvice>('GET', `/api/servers/${(await server('cobblemon')).id}/memory`)).toMatchObject({ verdict: 'raise', recommendedMB: 6144 })
})

it('crashed Cobblemon out of memory, and giving it 6 GB starts it again', async () => {
  const before = await server('cobblemon')
  expect(before).toMatchObject({ type: 'fabric', phase: 'crashed', crash: { kind: 'heap_out_of_memory', roomMB: 11776 } })
  expect(before.crash?.fixes.map((f) => f.kind)).toEqual(['raise_memory', 'restart'])
  await ask('POST', `/api/servers/${before.id}/settings`, { memoryMB: 6144 })
  await ask('POST', `/api/servers/${before.id}/start`)
  await vi.advanceTimersByTimeAsync(20_000)
  const after = await server('cobblemon')
  expect(after).toMatchObject({ phase: 'online', config: { memoryMB: 6144 } })
  expect(after.crash).toBeUndefined()
})

it('lists Cobblemon’s mods, and what friends need of each', async () => {
  const { id } = await server('cobblemon')
  const list = await ask<Addons>('GET', `/api/servers/${id}/addons`)
  expect(list.target).toMatchObject({ kind: 'mod', folder: 'mods', sources: ['modrinth'] })
  expect(list.files.map((f) => f.addon?.name)).toEqual(['Fabric API', 'Cobblemon', 'Lithium'])
  expect((await ask<AddonBrowse>('GET', `/api/servers/${id}/addons/search`)).cards.map((c) => c.name)).toEqual(['Fabric API', 'Lithium', 'Cobblemon'])
  const share = await ask<PackShare>('GET', `/api/servers/${id}/mods/share`)
  expect(share).toMatchObject({ public: false, loaderName: 'Fabric', share: { type: 'fabric', loaderVersion: '0.19.3', notice: { key: 'share.notice.two' } } })
  expect(share.share.mods.map((m) => [m.name, m.need])).toEqual([
    ['Fabric API', 'required'],
    ['Cobblemon', 'required'],
    ['Lithium', 'optional'],
  ])
  await expect(ask('POST', `/api/servers/${id}/mods/share`, { public: true })).rejects.toMatchObject({ status: 400, code: 'demo' })
  await expect(ask('GET', `/api/servers/${(await survival()).id}/mods/share`)).rejects.toMatchObject({ status: 404 })
})

it('picks a few plugins for Paper servers, and none for Fabric', async () => {
  const picks = await ask<CuratedAddons>('GET', `/api/servers/${(await survival()).id}/addons/curated`)
  expect(picks.picks.map((p) => [p.id, p.card.name, p.card.installed])).toEqual([
    ['rollback', 'CoreProtect', true],
    ['pregenerate', 'Chunky', true],
    ['newer-clients', 'ViaVersion', true],
    ['essentials', 'EssentialsX', false],
    ['permissions', 'LuckPerms', true],
    ['lag-finder', 'spark', false],
  ])
  expect((await ask<CuratedAddons>('GET', `/api/servers/${(await server('cobblemon')).id}/addons/curated`)).picks).toEqual([])
})

it('offers every server type, with its builds, and makes a Fabric server as asked', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  const paper = await ask<Catalog>('GET', `/api/machines/${m?.id}/catalog`)
  expect(paper.types.map((x) => x.id)).toEqual(['paper', 'purpur', 'fabric', 'quilt', 'neoforge', 'forge', 'vanilla'])
  const fabric = await ask<Catalog>('GET', `/api/machines/${m?.id}/catalog?type=fabric`)
  expect(fabric.type).toBe('fabric')
  expect(fabric.versions.every((v) => v.software?.type === 'fabric')).toBe(true)
  const builds = await ask<SoftwareBuilds>('GET', `/api/machines/${m?.id}/catalog/builds?type=fabric&version=26.1.2`)
  expect(builds.builds[0]).toMatchObject({ version: '0.19.3', recommended: true })
  const version = fabric.versions.find((v) => v.recommended)
  await ask('POST', `/api/machines/${m?.id}/servers`, { name: 'Skyblock', type: 'fabric', versionId: version?.id, memoryMB: 2048 })
  expect(await server('skyblock')).toMatchObject({ type: 'fabric', config: { software: { type: 'fabric' } } })
})

it('has room for New server’s suggested memory, so its default creates a server', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  const catalog = await ask<Catalog>('GET', `/api/machines/${m?.id}/catalog`)
  expect(catalog.recommendedMemoryMB).toBeLessThanOrEqual(catalog.memoryFreeMB)
  const largest = Math.max(...(catalog.sizing?.suggestions ?? []).filter((x) => x.players <= 10).map((x) => x.memoryMB))
  expect(largest).toBeLessThanOrEqual(catalog.memoryFreeMB)
  await ask('POST', `/api/machines/${m?.id}/servers`, { name: 'Skyblock', versionId: catalog.versions.find((v) => v.recommended)?.id, memoryMB: catalog.recommendedMemoryMB })
  expect(await server('skyblock')).toMatchObject({ type: 'paper', config: { memoryMB: catalog.recommendedMemoryMB } })
})

it('keeps a Fabric server on Fabric: its versions, its upgrades and a version change', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  const cobblemon = await server('cobblemon')
  expect(cobblemon.config).toMatchObject({ type: 'fabric', versionId: 'fabric-26.1.2', paperBuild: 0 })
  const catalog = await ask<Catalog>('GET', `/api/machines/${m?.id}/catalog?server=${cobblemon.id}`)
  expect(catalog.type).toBe('fabric')
  expect(upgradeTargets(cobblemon.config!, catalog.versions).map((v) => v.id)).toEqual(['fabric-26.2.1'])
  await expect(ask('POST', `/api/servers/${cobblemon.id}/version`, { versionId: 'paper-26.2.1' })).rejects.toMatchObject({ status: 400 })
  await ask('POST', `/api/servers/${cobblemon.id}/version`, { versionId: 'fabric-26.2.1' })
  await vi.advanceTimersByTimeAsync(30_000)
  expect((await server('cobblemon')).config).toMatchObject({ type: 'fabric', versionId: 'fabric-26.2.1', minecraftVersion: '26.2.1', software: { type: 'fabric' } })
})

it('gives Quilt, NeoForge and Forge servers mods, not plugins', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  for (const type of ['quilt', 'neoforge', 'forge']) {
    await ask('POST', `/api/machines/${m?.id}/servers`, { name: `A ${type} world`, type, memoryMB: 2048 })
    const { id } = await server(`a-${type}-world`)
    expect((await ask<Addons>('GET', `/api/servers/${id}/addons`)).target.kind).toBe('mod')
    expect((await ask<AddonBrowse>('GET', `/api/servers/${id}/addons/search`)).cards.every((c) => ['Fabric API', 'Cobblemon', 'Lithium'].includes(c.name))).toBe(true)
  }
  expect((await ask<PackShare>('GET', `/api/servers/${(await server('a-quilt-world')).id}/mods/share`)).loaderName).toBe('Quilt')
  expect(await ask<PackShare>('GET', `/api/servers/${(await server('a-forge-world')).id}/mods/share`)).toMatchObject({ loaderName: 'Forge', share: { type: 'forge', loaderVersion: '64.1.3' } })
})

it('keeps every type’s version, build and loader in step with the catalog entry chosen, through create and a version change', async () => {
  const [m] = await ask<{ id: string }[]>('GET', '/api/machines')
  const agrees = (srv: ServerStatus, type: string, entry: CatalogEntry, build: string | undefined, step: string) => {
    const where = `${type} ${step} ${entry.id}`
    expect(srv.type, where).toBe(type)
    expect(srv.config, where).toMatchObject({ type, versionId: entry.id, minecraftVersion: entry.minecraftVersion })
    if (type === 'paper') {
      expect(srv.config?.paperBuild, where).toBe(entry.paperBuild)
      expect(srv.config?.software, where).toBeUndefined()
      return
    }
    expect(srv.config?.software, where).toMatchObject({ type, minecraftVersion: entry.minecraftVersion })
    expect(pinBuild(srv.config?.software), where).toBe(build ?? entry.build ?? '')
  }
  let n = 0
  for (const type of ['paper', 'purpur', 'fabric', 'quilt', 'neoforge', 'forge', 'vanilla']) {
    const catalog = await ask<Catalog>('GET', `/api/machines/${m?.id}/catalog?type=${type}`)
    expect(catalog.versions.every((v) => hasBuilds(type) === !!v.build), type).toBe(true)
    const newest = catalog.versions[0]!
    for (const [i, from] of catalog.versions.slice(1).entries()) {
      const offered = hasBuilds(type) ? (await ask<SoftwareBuilds>('GET', `/api/machines/${m?.id}/catalog/builds?type=${type}&version=${from.minecraftVersion}`)).builds : []
      // Every other server takes a build other than the recommended one.
      const build = i % 2 ? offered[1]?.version : undefined
      const name = `Table ${++n}`
      await ask('POST', `/api/machines/${m?.id}/servers`, { name, type, versionId: from.id, memoryMB: 512, ...(build ? { build } : {}) })
      await vi.advanceTimersByTimeAsync(30_000)
      const made = await server(`table-${n}`)
      agrees(made, type, from, build, 'created')
      expect(upgradeTargets(made.config!, catalog.versions).map((v) => v.id), type).toContain(newest.id)
      await ask('POST', `/api/servers/${made.id}/version`, { versionId: newest.id })
      await vi.advanceTimersByTimeAsync(30_000)
      agrees(await server(`table-${n}`), type, newest, undefined, 'changed')
    }
  }
})
