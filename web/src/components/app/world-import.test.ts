import { describe, expect, it } from 'vitest'
import type { ImportMessage, ImportPreview, ImportWorld, WorldImport, WorldImportPreview } from '@/api/types'
import { checkRows, defaultWorld, serverNameFrom, suggestedName, versionChange, worldName } from './world-import'

function importWorld(id: string, over: Partial<ImportWorld> = {}): ImportWorld {
  return { id, archive: 'Survival-2024.zip', path: `Survival-2024/${id}`, origin: 'singleplayer', dimensions: ['minecraft:overworld'], players: 0, sizeBytes: 100, files: 10, ...over }
}

function upload(worlds: ImportWorld[], warnings?: ImportMessage[]): WorldImport {
  return { id: 'imp2345abc', createdAt: '2026-09-25T10:00:00Z', files: [], limitBytes: 2 ** 34, inspection: { archives: [], worlds, warnings } }
}

function check(over: Partial<ImportPreview> = {}): WorldImportPreview {
  const w = importWorld('w1', { level: { name: 'Survival 2024', version: '1.21.4', hardcore: false, spawn: { x: 40, z: 12 } } })
  return {
    id: 'imp2345abc',
    preview: {
      world: w,
      target: { type: 'paper', minecraftVersion: '26.1.2', levelName: 'world' },
      version: { compat: 'upgrade', world: '1.21.4', target: '26.1.2' },
      folders: ['world', 'world_nether', 'world_the_end'],
      fileCount: 120,
      sizeBytes: 1.2 * 2 ** 30,
      dimensions: [
        { id: 'minecraft:overworld', folder: 'world', files: 80, bytes: 1 },
        { id: 'minecraft:the_nether', folder: 'world_nether', files: 20, bytes: 1 },
        { id: 'minecraft:the_end', folder: 'world_the_end', files: 20, bytes: 1 },
      ],
      dataPacks: ['vanilla', 'file/Multiplayer sleep.zip', 'file/Graves.zip'],
      players: 3,
      ...over,
    },
    versions: [],
    versionId: 'paper-26.1.2',
    keepsOriginal: true,
  }
}

const message = (kind: string, text: string, hint?: string): ImportMessage => ({ kind, text, hint })

describe('world import', () => {
  it('previews the world the upload names first, else the largest', () => {
    expect(defaultWorld(upload([importWorld('a', { sizeBytes: 5 }), importWorld('b', { sizeBytes: 1, default: true })]))).toBe('b')
    expect(defaultWorld(upload([importWorld('a', { sizeBytes: 5, default: true }), importWorld('b', { sizeBytes: 9, default: true })]))).toBe('b')
    expect(defaultWorld(upload([importWorld('a', { sizeBytes: 5 }), importWorld('b', { sizeBytes: 9 })]))).toBe('b')
    expect(defaultWorld({ ...upload([]), inspection: undefined })).toBeUndefined()
  })

  it('names a world after its level, its folder or its archive', () => {
    expect(worldName(importWorld('a', { level: { name: '  Island ', hardcore: false } }))).toBe('Island')
    expect(worldName(importWorld('a', { path: 'server/world/' }))).toBe('world')
    expect(worldName(importWorld('a', { path: '' }))).toBe('Survival-2024.zip')
  })

  it('makes server names without colour codes, control characters or more than 32 characters', () => {
    expect(serverNameFrom('§6Gold §lRush')).toBe('Gold Rush')
    expect(serverNameFrom('Tab\there\u200b')).toBe('Tabhere')
    expect(Array.from(serverNameFrom('🌋'.repeat(40)))).toHaveLength(32)
  })

  it('suggests the world’s name, skipping Minecraft’s default names', () => {
    const files = [new File([], 'Survival-2024.zip')]
    const named = upload([importWorld('a', { level: { name: 'Island', hardcore: false } })])
    expect(suggestedName(named, 'a', files)).toBe('Island')
    const plain = upload([importWorld('a', { level: { name: 'world', hardcore: false } })])
    expect(suggestedName(plain, 'a', files)).toBe('Survival-2024')
    expect(suggestedName(plain, 'a', [new File([], 'World.tar.gz')])).toBe('world')
    expect(suggestedName(upload([]), undefined, [])).toBe('')
  })

  it('lists what the check found', () => {
    const rows = checkRows(check())
    expect(rows.map((r) => [r.tone, r.title, r.detail])).toEqual([
      ['ok', 'The world, the Nether and the End', '1.2 GB · spawn at x 40, z 12 · seed and game rules kept'],
      ['ok', '2 data packs', 'Multiplayer sleep and Graves. They stay on.'],
      ['note', 'No plugins inside', 'Add plugins again after it starts.'],
      ['note', 'Made in Minecraft 1.21.4', undefined],
    ])
    const folders = checkRows(check({ dataPacks: ['file/multiplayer-sleep', 'file/graves_v2.zip', 'paper'] }))
    expect(folders[1]?.detail).toBe('Multiplayer sleep and Graves v2. They stay on.')
  })

  it('shows warnings once, skips the ones said elsewhere, and marks problems', () => {
    const p = check({
      dimensions: [
        { id: 'minecraft:overworld', folder: 'world', files: 1, bytes: 1 },
        { id: 'twilightforest:twilight_forest', folder: 'world/dimensions/twilightforest', files: 1, bytes: 1 },
      ],
      dataPacks: [],
      version: { compat: 'same', world: '26.1.2', target: '26.1.2' },
      leftOut: [{ kind: 'addons', files: 12, bytes: 1, text: '12 plugin files are left out.' }],
      warnings: [message('world_upgrade', 'The world is upgraded.'), message('modded_dimensions', 'One dimension comes from a mod.')],
      problems: [message('too_big', 'The world is too big.', 'Free disk space.')],
    })
    const w = { ...p.preview.world, level: { name: 'Island', version: '26.1.2', hardcore: false } }
    p.preview.world = w
    const rows = checkRows(p, upload([w], [message('modded_dimensions', 'One dimension comes from a mod.')]))
    expect(rows.map((r) => [r.tone, r.title])).toEqual([
      ['ok', 'The world and 1 more dimension'],
      ['note', 'Plugins aren’t copied'],
      ['ok', 'Made in Minecraft 26.1.2'],
      ['note', 'One dimension comes from a mod.'],
      ['problem', 'The world is too big.'],
    ])
    expect(rows[0]?.detail).toBe('1.2 GB · seed and game rules kept')
    expect(rows[4]?.detail).toBe('Free disk space.')
  })

  it('shows the version change in the summary', () => {
    expect(versionChange(check())).toBe('1.21.4 → 26.1.2')
    expect(versionChange(check({ target: { type: 'paper', minecraftVersion: '1.21.4', levelName: 'world' }, version: { compat: 'same', world: '1.21.4', target: '1.21.4' } }))).toBe('1.21.4')
  })
})
