import { describe, expect, it } from 'vitest'
import type { MapWorld } from '@/api/types'
import { addedWorldsLabel, clampZoom, coord, formatCoords, guessWorlds, hasMap, ownWorld, packName, screenToWorld, sortWorlds, visibleTiles, worldLabel, worldToScreen, zoomAround } from './map'

function world(name: string, dimension: MapWorld['dimension'], over: Partial<MapWorld> = {}): MapWorld {
  return { name, dimension, label: name, spawn: { x: 0, z: 0 }, zoom: { max: 3, default: 3, extra: 2 }, refreshSeconds: 5, ...over }
}

describe('map worlds', () => {
  it('has no map on Vanilla', () => {
    expect(hasMap({ type: 'paper' })).toBe(true)
    expect(hasMap({ type: 'fabric' })).toBe(true)
    expect(hasMap({ type: 'vanilla' })).toBe(false)
    expect(hasMap({ type: '' })).toBe(true)
  })

  it('tells the server’s own worlds from added ones', () => {
    expect(ownWorld(world('world', 'overworld'))).toBe(true)
    expect(ownWorld(world('survival_nether', 'nether'), 'survival')).toBe(true)
    expect(ownWorld(world('world_nether', 'nether'), 'survival')).toBe(false)
    expect(ownWorld(world('minecraft_the_end', 'end'))).toBe(true)
    expect(ownWorld(world('mining', 'overworld'))).toBe(false)
    expect(ownWorld(world('world', 'custom'))).toBe(false)
  })

  it('sorts the own worlds first, then the added ones by name', () => {
    const list = [world('mining', 'overworld'), world('world_the_end', 'end'), world('creative', 'overworld'), world('world_nether', 'nether'), world('world', 'overworld')]
    expect(sortWorlds(list).map((w) => w.name)).toEqual(['world', 'world_nether', 'world_the_end', 'creative', 'mining'])
  })

  it('labels worlds for the toggle, the select and the long form', () => {
    expect(worldLabel(world('world', 'overworld'))).toBe('Overworld')
    expect(worldLabel(world('world_nether', 'nether'))).toBe('Nether')
    expect(worldLabel(world('world_nether', 'nether'), undefined, 'long')).toBe('The Nether')
    expect(worldLabel(world('world_the_end', 'end'))).toBe('The End')
    expect(worldLabel(world('world_the_end', 'end'), undefined, 'short')).toBe('End')
    expect(worldLabel(world('mining', 'overworld'))).toBe('mining')
    expect(worldLabel(world('twilightforest_twilight_forest', 'custom', { label: 'Twilight Forest' }))).toBe('Twilight Forest')
  })

  it('guesses the level name and type from the world names alone', () => {
    expect(guessWorlds([world('survival', 'overworld'), world('survival_nether', 'nether'), world('mining', 'overworld')])).toEqual({ levelName: 'survival', serverType: 'paper' })
    expect(guessWorlds([world('minecraft_overworld', 'overworld'), world('minecraft_the_nether', 'nether')])).toEqual({ levelName: undefined, serverType: 'fabric' })
    expect(addedWorldsLabel('paper')).toBe('Added by a plugin')
    expect(addedWorldsLabel('fabric')).toBe('Added by a mod or data pack')
  })

  it('writes coordinates as F3 does and names data packs', () => {
    expect(coord(1234.6)).toBe('1235')
    expect(coord(-40)).toBe('−40')
    expect(formatCoords(12345, -7)).toBe('x 12345 · z −7')
    expect(packName('file/Graves.zip')).toBe('Graves')
    expect(packName('file/Multiplayer sleep')).toBe('Multiplayer sleep')
  })
})

describe('map view', () => {
  const w = world('world', 'overworld')

  it('keeps zoom between the drawn levels and the extra ones', () => {
    expect(clampZoom(-2, w)).toBe(0)
    expect(clampZoom(9, w)).toBe(5)
    expect(clampZoom(4, w)).toBe(4)
  })

  it('maps blocks to the screen and back', () => {
    const v = { x: 100, z: -50, zoom: 4 }
    const at = worldToScreen(v, 800, 600, w, 110, -50)
    expect(at).toEqual({ left: 420, top: 300 })
    expect(screenToWorld(v, 800, 600, w, at.left, at.top)).toEqual({ x: 110, z: -50 })
  })

  it('keeps the block under the cursor in place when zooming', () => {
    const v = { x: 0, z: 0, zoom: 3 }
    const before = screenToWorld(v, 800, 600, w, 600, 100)
    const next = zoomAround(v, 800, 600, w, 5, 600, 100)
    expect(next.zoom).toBe(5)
    expect(screenToWorld(next, 800, 600, w, 600, 100)).toEqual(before)
  })

  it('lists the tiles that cover the view', () => {
    const tiles = visibleTiles({ x: 0, z: 0, zoom: 3 }, 1000, 500, w, 512)
    expect(tiles.map((t) => [t.x, t.z])).toEqual([
      [-1, -1],
      [0, -1],
      [-1, 0],
      [0, 0],
    ])
    expect(tiles[3]).toMatchObject({ tz: 3, left: 500, top: 250, size: 512 })
    const far = visibleTiles({ x: 0, z: 0, zoom: 1 }, 1024, 512, w, 512)
    expect(far[0]).toMatchObject({ tz: 1, size: 512 })
    expect(visibleTiles({ x: 0, z: 0, zoom: 3 }, 1e6, 1e6, w, 512)).toEqual([])
  })
})
