import { describe, expect, it } from 'vitest'
import type { Addon, AddonCard, AddonChecks, AddonDetails, AddonNotice, AddonPlan, AddonStep, Addons, Operation } from '@/api/types'
import {
  addonKind,
  addonTab,
  alsoInstalls,
  appendCards,
  checksumFailed,
  compactCount,
  footerFor,
  isAddonOp,
  maxSearch,
  mergeRows,
  opFiles,
  opNotice,
  opRestartNeeded,
  pendingCount,
  searchPath,
  updateKeys,
  updatableRows,
  updatedAgo,
  versionPage,
} from './addons'

function addon(name: string, over: Partial<Addon> = {}): Addon {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return {
    source: 'modrinth',
    projectId: id,
    slug: id,
    name,
    summary: `${name} summary`,
    versionId: `${id}-v1`,
    versionNumber: '1.0.0',
    channel: 'release',
    published: '2026-09-01T00:00:00Z',
    fileName: `${name}-1.0.0.jar`,
    size: 1000,
    installedAt: '2026-09-02T00:00:00Z',
    ...over,
  }
}

function folder(over: Partial<Addons> = {}): Addons {
  return {
    target: { kind: 'plugin', folder: 'plugins', sources: ['modrinth', 'hangar'], categories: ['admin', 'world'], minecraftVersion: '26.1.2' },
    files: [],
    missing: [],
    warnings: [],
    restartNeeded: false,
    ...over,
  }
}

function card(name: string, over: Partial<AddonCard> = {}): AddonCard {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return { source: 'modrinth', projectId: id, slug: id, name, summary: '', categories: [], downloads: 10, updated: '2026-09-01T00:00:00Z', pageUrl: `https://modrinth.com/plugin/${id}`, installed: false, ...over }
}

function plan(over: Partial<AddonPlan> = {}): AddonPlan {
  return { steps: [], manual: [], blockers: [], warnings: [], ready: true, fingerprint: 'fp1', ...over }
}

function step(name: string, over: Partial<AddonStep> = {}): AddonStep {
  const id = name.toLowerCase().replace(/[^a-z]/g, '')
  return { action: 'install', source: 'modrinth', projectId: id, name, versionNumber: '1.0.0', channel: 'release', fileName: `${name}.jar`, size: 10, ...over }
}

function details(over: Partial<AddonDetails> = {}): AddonDetails {
  return { card: card('Multiverse-Portals'), latest: { versionId: 'v5', versionNumber: '5.0.1', channel: 'release', published: '2026-09-01T00:00:00Z' }, ...over }
}

const note = (kind: string, params: Record<string, string> = {}, url?: string): AddonNotice => ({ kind, params, message: `${kind} message`, hint: `${kind} hint`, url })

describe('add-on kinds', () => {
  it('names the tab after what the server type runs', () => {
    expect(addonKind('paper')).toBe('plugin')
    expect(addonKind('purpur')).toBe('plugin')
    expect(addonKind('fabric')).toBe('mod')
    expect(addonKind('neoforge')).toBe('mod')
    expect(addonKind('vanilla')).toBeUndefined()
    expect(addonKind(undefined)).toBeUndefined()
    expect(addonTab('paper')).toBe('plugins')
    expect(addonTab('quilt')).toBe('mods')
    expect(addonTab('vanilla')).toBeUndefined()
  })
})

describe('mergeRows', () => {
  const chunky = addon('Chunky', { source: 'hangar', projectId: '81' })
  const core = addon('Multiverse-Core')
  const portals = addon('Multiverse-Portals', { dependencyOf: 'multiversecore' })
  const luck = addon('LuckPerms')

  it('lists every file, then the records whose file is gone, by name', () => {
    const rows = mergeRows(
      folder({
        files: [
          { fileName: 'zeta.jar', size: 1, status: 'unknown', name: 'Zeta', version: '2.0' },
          { fileName: chunky.fileName, size: 1, status: 'managed', addon: chunky, pending: true },
          { fileName: core.fileName, size: 1, status: 'modified', addon: core },
          { fileName: 'essx.jar', size: 1, status: 'unknown' },
        ],
        missing: [portals],
      }),
    )
    expect(rows.map((r) => [r.name, r.state])).toEqual([
      ['Chunky', 'managed'],
      ['essx', 'unknown'],
      ['Multiverse-Core', 'changed'],
      ['Multiverse-Portals', 'missing'],
      ['Zeta', 'unknown'],
    ])
    expect(rows[0]).toMatchObject({ id: 'hangar:81', pending: true, version: '1.0.0', fileName: chunky.fileName })
    expect(rows[1]).toMatchObject({ id: 'file:essx.jar', version: '' })
    expect(rows[1]?.addon).toBeUndefined()
    expect(rows[3]).toMatchObject({ id: 'modrinth:multiverseportals', fileName: portals.fileName, pending: false })
    expect(rows[4]).toMatchObject({ version: '2.0', fileName: 'zeta.jar' })
  })

  it('marks files added by hand that the library recognised, and available updates', () => {
    const checks: AddonChecks = {
      updates: [
        { source: 'hangar', projectId: '81', available: true, latest: { versionId: 'c2', versionNumber: '1.4.40', channel: 'release', published: '2026-09-20T00:00:00Z' } },
        { source: 'modrinth', projectId: 'luckperms', available: false, latest: { versionId: 'l1', versionNumber: '1.0.0', channel: 'release', published: '2026-09-01T00:00:00Z' } },
      ],
      identified: [{ fileName: 'Vault.jar', size: 1, status: 'identified', addon: addon('Vault') }],
      checkedAt: '2026-09-25T00:00:00Z',
    }
    const rows = mergeRows(
      folder({
        files: [
          { fileName: chunky.fileName, size: 1, status: 'managed', addon: chunky },
          { fileName: luck.fileName, size: 1, status: 'managed', addon: luck },
          { fileName: 'Vault.jar', size: 1, status: 'unknown', name: 'Vault', version: '1.7' },
        ],
      }),
      checks,
    )
    const byName = Object.fromEntries(rows.map((r) => [r.name, r]))
    expect(byName.Chunky?.update?.versionNumber).toBe('1.4.40')
    expect(byName.LuckPerms?.update).toBeUndefined()
    expect(byName.Vault).toMatchObject({ state: 'identified', id: 'file:Vault.jar', fileName: 'Vault.jar', version: '1.0.0' })
    expect(byName.Vault?.addon?.projectId).toBe('vault')
  })

  it('updates all unchanged add-ons with updates, sending only their keys', () => {
    const upd = { versionId: 'x', versionNumber: '2.0', channel: 'release', published: '' }
    const rows = mergeRows(
      folder({
        files: [
          { fileName: chunky.fileName, size: 1, status: 'managed', addon: chunky, pending: true },
          { fileName: core.fileName, size: 1, status: 'modified', addon: core },
          { fileName: luck.fileName, size: 1, status: 'managed', addon: luck },
        ],
      }),
      {
        updates: [
          { source: 'hangar', projectId: '81', available: true, latest: upd },
          { source: 'modrinth', projectId: 'multiversecore', available: true, latest: upd },
        ],
        identified: [],
        checkedAt: '',
      },
    )
    expect(updatableRows(rows).map((r) => r.name)).toEqual(['Chunky'])
    expect(updateKeys(rows)).toEqual([{ source: 'hangar', projectId: '81' }])
    expect(pendingCount(rows)).toBe(1)
  })
})

describe('footerFor', () => {
  const upd = { versionId: 'c2', versionNumber: '1.4.40', channel: 'release', published: '' }

  it('offers the installed actions', () => {
    expect(footerFor(details({ installed: addon('Chunky'), updateAvailable: true, latest: upd }))).toEqual({ kind: 'installed', update: upd, changed: false, missing: false })
    expect(footerFor(details({ installed: addon('Chunky'), latest: upd }))).toEqual({ kind: 'installed', update: undefined, changed: false, missing: false })
    expect(footerFor(details({ installed: addon('Chunky'), changed: true, missing: false }))).toMatchObject({ changed: true })
    expect(footerFor(details({ installed: addon('Chunky'), missing: true }))).toMatchObject({ missing: true })
  })

  it('sends people to the author for add-ons only on their site', () => {
    expect(footerFor(details({ latest: { ...upd, externalUrl: 'https://example.com/dynmap' } }))).toEqual({ kind: 'external', url: 'https://example.com/dynmap' })
    expect(footerFor(details({ plan: plan({ manual: [note('external_download', { name: 'Dynmap' }, 'https://example.com/d')] }) }))).toEqual({ kind: 'external', url: 'https://example.com/d' })
  })

  it('points at an installed conflict, and at dependencies from other sites', () => {
    const conflict = note('conflict', { name: 'Plasmo Voice', other: 'Simple Voice Chat', file: 'voicechat.jar' })
    expect(footerFor(details({ plan: plan({ ready: false, blockers: [conflict] }) }))).toEqual({ kind: 'conflict', other: 'Simple Voice Chat', file: 'voicechat.jar' })
    const inPlan = note('conflict', { name: 'A', other: 'B' })
    expect(footerFor(details({ plan: plan({ ready: false, blockers: [inPlan] }) }))).toEqual({ kind: 'blocked', notice: inPlan })
    expect(footerFor(details({ plan: plan({ manual: [note('dependency_external', { name: 'ChestShop', dependency: 'Vault', folder: 'plugins' }, 'https://example.com/vault')] }) }))).toEqual({
      kind: 'needs',
      dependency: 'Vault',
      url: 'https://example.com/vault',
    })
    expect(footerFor(details({ plan: plan({ manual: [note('dependency_unlisted', { name: 'ChestShop', file: 'Vault.jar', folder: 'plugins' })] }) }))).toEqual({ kind: 'needs', dependency: 'Vault.jar', url: undefined })
  })

  it('installs a ready plan with its fingerprint, and says why others are blocked', () => {
    expect(footerFor(details({ plan: plan({ fingerprint: 'abc' }) }))).toEqual({ kind: 'install', fingerprint: 'abc' })
    const gone = note('dependency_unavailable', { name: 'X', dependency: 'Y', source: 'Modrinth' })
    expect(footerFor(details({ plan: plan({ ready: false, blockers: [gone] }) }))).toEqual({ kind: 'blocked', notice: gone })
    const err = note('unreachable')
    expect(footerFor(details({ planError: err }))).toEqual({ kind: 'blocked', notice: err })
    expect(footerFor(details({ notice: err }))).toEqual({ kind: 'blocked', notice: err })
  })

  it('lists the dependencies an install brings', () => {
    const d = details({ plan: plan({ steps: [step('Multiverse-Core', { neededBy: 'Multiverse-Portals' }), step('Multiverse-Portals')] }) })
    expect(alsoInstalls(d).map((s) => s.name)).toEqual(['Multiverse-Core'])
    expect(alsoInstalls(details())).toEqual([])
  })
})

describe('library helpers', () => {
  it('links to a version’s notes on its source', () => {
    const v = { versionId: 'AbC1', versionNumber: '1.4.40', channel: 'release', published: '' }
    expect(versionPage(card('Chunky'), v)).toBe('https://modrinth.com/plugin/chunky/version/AbC1')
    expect(versionPage(card('Chunky', { source: 'hangar', pageUrl: 'https://hangar.papermc.io/pop4959/Chunky/' }), v)).toBe('https://hangar.papermc.io/pop4959/Chunky/versions/1.4.40')
    expect(versionPage(card('Chunky', { pageUrl: '' }), v)).toBeUndefined()
  })

  it('builds the search the agent reads', () => {
    expect(searchPath('abc', { q: '', category: '', sort: 'downloads' })).toBe('/api/servers/abc/addons/search')
    expect(searchPath('abc', { q: '  web map ', category: 'world', sort: 'relevance' }, 2)).toBe('/api/servers/abc/addons/search?q=web+map&category=world&sort=relevance&page=2')
    expect(searchPath('abc', { q: 'a&b', category: '', sort: 'updated' }, 0)).toBe('/api/servers/abc/addons/search?q=a%26b&sort=updated')
    const long = new URL(searchPath('abc', { q: '🧱'.repeat(maxSearch + 5), category: '', sort: 'downloads' }), 'https://x').searchParams.get('q') ?? ''
    expect([...long]).toHaveLength(maxSearch)
  })

  it('drops an add-on that comes back on a later page from its other listing', () => {
    const first = [card('BlueMap'), card('Dynmap')]
    const next = [card('BlueMap'), card('Blue Map', { source: 'hangar', projectId: '9' }), card('Chunky', { source: 'hangar', projectId: '81' })]
    expect(appendCards(first, next).map((c) => `${c.source}:${c.name}`)).toEqual(['modrinth:BlueMap', 'modrinth:Dynmap', 'hangar:Chunky'])
    expect(appendCards([], [card('A'), card('A')])).toHaveLength(1)
  })

  it('shortens download counts and dates', () => {
    expect(compactCount(2_100_000)).toBe('2.1M')
    expect(compactCount(380_000)).toBe('380K')
    expect(compactCount(950)).toBe('950')
    const now = Date.parse('2026-09-25T12:00:00Z')
    expect(updatedAgo('2026-09-20T12:00:00Z', now)).toBe('5 days ago')
    expect(updatedAgo('2026-09-11T12:00:00Z', now)).toBe('2 weeks ago')
    expect(updatedAgo('2026-06-01T12:00:00Z', now)).toBe('3 months ago')
    expect(updatedAgo('2024-01-01T12:00:00Z', now)).toBe('2 years ago')
    expect(updatedAgo('not a date', now)).toBe('')
  })
})

describe('add-on operations', () => {
  const op = (over: Partial<Operation>): Operation => ({ id: 'op1', kind: 'addon-install', status: 'running', phase: 'downloading', actor: 'siya', startedAt: '', ...over })

  it('reads the files, the notice and the restart from the detail', () => {
    const files = [{ name: 'Chunky', versionNumber: '1.4.40', was: '1.4.36', size: 100, received: 50, state: 'downloading' }]
    expect(isAddonOp(op({}))).toBe(true)
    expect(isAddonOp(op({ kind: 'addon-update' }))).toBe(true)
    expect(isAddonOp(op({ kind: 'backup' }))).toBe(false)
    expect(isAddonOp(undefined)).toBe(false)
    expect(opFiles(op({ detail: { files } }))).toEqual(files)
    expect(opFiles(op({ detail: { files: 'nope' } }))).toEqual([])
    expect(opRestartNeeded(op({ status: 'succeeded', detail: { restartNeeded: true } }))).toBe(true)
    expect(opRestartNeeded(op({ status: 'succeeded', detail: {} }))).toBe(false)
    const bad = note('hash_mismatch', { name: 'Multiverse-Portals', file: 'p.jar', source: 'Modrinth' })
    expect(opNotice(op({ status: 'failed', detail: { notice: bad } }))).toEqual(bad)
    expect(opNotice(op({ detail: { notice: 'text' } }))).toBeUndefined()
    expect(checksumFailed(bad)).toBe(true)
    expect(checksumFailed(note('size_mismatch'))).toBe(true)
    expect(checksumFailed(note('unreachable'))).toBe(false)
  })
})
