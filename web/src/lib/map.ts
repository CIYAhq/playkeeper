import type { MapWorld, ServerStatus } from '@/api/types'
import { t } from '@/i18n'

/** The server types squaremap runs on. Vanilla runs no plugins or mods, so it has no map. */
const mapTypes = ['paper', 'purpur', 'fabric', 'quilt', 'neoforge']
const pluginTypes = ['paper', 'purpur']

export function hasMap(s: Pick<ServerStatus, 'type'>): boolean {
  return mapTypes.includes(s.type || 'paper')
}

/**
 * Is a world one of the server's own Overworld, Nether and End, rather than
 * one a plugin, mod or data pack added? Paper names them after level-name,
 * mod loaders after the dimension (minecraft_overworld).
 */
export function ownWorld(w: MapWorld, levelName = 'world'): boolean {
  switch (w.dimension) {
    case 'overworld':
      return w.name === levelName || w.name === 'minecraft_overworld'
    case 'nether':
      return w.name === `${levelName}_nether` || w.name === 'minecraft_the_nether'
    case 'end':
      return w.name === `${levelName}_the_end` || w.name === 'minecraft_the_end'
    case 'custom':
      return false
    default: {
      const unreachable: never = w.dimension
      return unreachable
    }
  }
}

const dimensionOrder = ['overworld', 'nether', 'end', 'custom']

/** The server's own worlds first (Overworld, Nether, End), then the added ones by name. */
export function sortWorlds(worlds: MapWorld[], levelName?: string): MapWorld[] {
  const rank = (w: MapWorld) => (ownWorld(w, levelName) ? dimensionOrder.indexOf(w.dimension) : 10)
  return [...worlds].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
}

/**
 * A world's name in the world switcher: "Overworld", "Nether", "The End"
 * for the server's own (long: "The Nether"; short: "End"), otherwise its
 * own name.
 */
export function worldLabel(w: MapWorld, levelName?: string, form: 'toggle' | 'short' | 'long' = 'toggle'): string {
  if (!ownWorld(w, levelName)) return w.dimension === 'custom' ? w.label || w.name : w.name
  switch (w.dimension) {
    case 'overworld':
      return t('map.overworld')
    case 'nether':
      return form === 'long' ? t('map.theNether') : t('map.nether')
    case 'end':
      return form === 'short' ? t('map.endShort') : t('map.end')
    case 'custom':
      return w.label || w.name
    default: {
      const unreachable: never = w.dimension
      return unreachable
    }
  }
}

/**
 * The level name and server type a world list implies, for the shared map,
 * which sees only the worlds: Paper names the Nether and End after the
 * Overworld (world_nether), mod loaders after the dimension.
 */
export function guessWorlds(worlds: MapWorld[]): { levelName?: string; serverType: string } {
  const names = new Set(worlds.map((w) => w.name))
  const over = worlds.find((w) => w.dimension === 'overworld' && (names.has(`${w.name}_nether`) || names.has(`${w.name}_the_end`)))
  return { levelName: over?.name, serverType: names.has('minecraft_overworld') ? 'fabric' : 'paper' }
}

/** Where a server's extra worlds come from, for the world select's second group. */
export function addedWorldsLabel(serverType: string): string {
  return pluginTypes.includes(serverType || 'paper') ? t('map.addedByPlugin') : t('map.addedByMod')
}

/** A block coordinate as players see it in F3: no grouping, a real minus sign. */
export function coord(n: number): string {
  const v = Math.round(n)
  return `${v < 0 ? '−' : ''}${Math.abs(v)}`
}

export function formatCoords(x: number, z: number): string {
  return t('map.coords', { x: coord(x), z: coord(z) })
}

// View geometry. A view is the block at the centre of the map and a zoom
// level: at zoom.max one screen pixel is one block, each level up doubles
// the size (up to zoom.max + zoom.extra), each level down halves it.

export interface MapViewState {
  x: number
  z: number
  zoom: number
}

export function minZoom(): number {
  return 0
}

export function maxZoom(w: MapWorld): number {
  return w.zoom.max + w.zoom.extra
}

export function clampZoom(zoom: number, w: MapWorld): number {
  return Math.min(Math.max(zoom, minZoom()), maxZoom(w))
}

/** Screen pixels per block at a zoom level. */
export function pixelsPerBlock(zoom: number, w: MapWorld): number {
  return 2 ** (zoom - w.zoom.max)
}

/** The zoom level of the tiles to draw: the nearest one squaremap drew. */
export function tileZoom(zoom: number, w: MapWorld): number {
  return Math.min(Math.max(Math.round(zoom), 0), w.zoom.max)
}

/** How many blocks one tile of a tile zoom level covers along each side. */
export function tileBlocks(tz: number, w: MapWorld, tileSize: number): number {
  return tileSize * 2 ** (w.zoom.max - tz)
}

export interface TilePlace {
  tz: number
  x: number
  z: number
  left: number
  top: number
  size: number
}

/** The tiles that cover a width × height view, with where they go on screen. */
export function visibleTiles(v: MapViewState, width: number, height: number, w: MapWorld, tileSize: number): TilePlace[] {
  const scale = pixelsPerBlock(v.zoom, w)
  const tz = tileZoom(v.zoom, w)
  const blocks = tileBlocks(tz, w, tileSize)
  const size = blocks * scale
  const x0 = v.x - width / 2 / scale
  const z0 = v.z - height / 2 / scale
  const out: TilePlace[] = []
  const first = { x: Math.floor(x0 / blocks), z: Math.floor(z0 / blocks) }
  const last = { x: Math.floor((x0 + width / scale) / blocks), z: Math.floor((z0 + height / scale) / blocks) }
  // A view never needs more than a screenful of tiles; the cap guards against bad input.
  if ((last.x - first.x + 1) * (last.z - first.z + 1) > 400) return out
  for (let tzz = first.z; tzz <= last.z; tzz++) {
    for (let tx = first.x; tx <= last.x; tx++) {
      out.push({ tz, x: tx, z: tzz, left: (tx * blocks - x0) * scale, top: (tzz * blocks - z0) * scale, size })
    }
  }
  return out
}

export function worldToScreen(v: MapViewState, width: number, height: number, w: MapWorld, bx: number, bz: number): { left: number; top: number } {
  const scale = pixelsPerBlock(v.zoom, w)
  return { left: width / 2 + (bx - v.x) * scale, top: height / 2 + (bz - v.z) * scale }
}

export function screenToWorld(v: MapViewState, width: number, height: number, w: MapWorld, left: number, top: number): { x: number; z: number } {
  const scale = pixelsPerBlock(v.zoom, w)
  return { x: v.x + (left - width / 2) / scale, z: v.z + (top - height / 2) / scale }
}

/** The view after zooming to `zoom` with the block under (left, top) staying put. */
export function zoomAround(v: MapViewState, width: number, height: number, w: MapWorld, zoom: number, left = width / 2, top = height / 2): MapViewState {
  const next = clampZoom(zoom, w)
  const anchor = screenToWorld(v, width, height, w, left, top)
  const scale = pixelsPerBlock(next, w)
  return { x: anchor.x - (left - width / 2) / scale, z: anchor.z - (top - height / 2) / scale, zoom: next }
}

/** A data pack's name as players know it: "file/Graves.zip" is "Graves". */
export function packName(id: string): string {
  return id.replace(/^file\//, '').replace(/\.(zip|jar)$/i, '')
}
