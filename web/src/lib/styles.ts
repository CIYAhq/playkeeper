import type { Gameplay, LevelType, PlayStyle } from '@/api/types'
import type { MessageKey } from '@/i18n'

/** A play style is a starting point: game settings and a preferred memory. */
export interface StylePreset {
  id: PlayStyle
  label: MessageKey
  title: MessageKey
  desc: MessageKey
  summary: MessageKey
  name: MessageKey
  memoryMB: number
  gameplay: Gameplay
}

export const stylePresets: StylePreset[] = [
  { id: 'friends', label: 'style.friends', title: 'style.friends.title', desc: 'style.friends.desc', summary: 'style.friends.summary', name: 'style.friends.name', memoryMB: 4096, gameplay: { difficulty: 'normal', pvp: false, gameMode: 'survival' } },
  { id: 'creative', label: 'style.creative', title: 'style.creative.title', desc: 'style.creative.desc', summary: 'style.creative.summary', name: 'style.creative.name', memoryMB: 3072, gameplay: { difficulty: 'peaceful', pvp: false, gameMode: 'creative' } },
  { id: 'hardcore', label: 'style.hardcore', title: 'style.hardcore.title', desc: 'style.hardcore.desc', summary: 'style.hardcore.summary', name: 'style.hardcore.name', memoryMB: 4096, gameplay: { difficulty: 'hard', pvp: true, gameMode: 'survival', hardcore: true } },
  { id: 'solo', label: 'style.solo', title: 'style.solo.title', desc: 'style.solo.desc', summary: 'style.solo.summary', name: 'style.solo.name', memoryMB: 2048, gameplay: { difficulty: 'normal', gameMode: 'survival' } },
]

export function preset(id: PlayStyle | '' | undefined): StylePreset | undefined {
  return stylePresets.find((p) => p.id === id)
}

export const levelTypes: LevelType[] = ['normal', 'flat', 'amplified', 'large_biomes']

/** The largest offered budget no bigger than the style wants, or the smallest offered. */
export function memoryForStyle(options: number[], wantMB: number): number {
  if (options.length === 0) return 0
  const fits = options.filter((mb) => mb <= wantMB)
  return fits.length ? Math.max(...fits) : Math.min(...options)
}

/**
 * What a mod loader and its mods take of a budget before its players, over
 * what Paper takes: the loader's memory outside the heap and the heap its
 * mods fill. A Quilt server with two mods at 2 GB was killed when one player
 * joined.
 */
const moddedMB: Record<string, number> = { fabric: 1024, quilt: 1024, neoforge: 2048 }

/** About how many players a memory budget suits on a server of the type, for the memory step. */
export function playersFor(memoryMB: number, type = 'paper'): number {
  const mb = memoryMB - (moddedMB[type] ?? 0)
  if (mb >= 8192) return 30
  if (mb >= 6144) return 20
  if (mb >= 4096) return 10
  if (mb >= 3072) return 6
  if (mb >= 2048) return 4
  if (mb >= 1024) return 2
  return 1
}
