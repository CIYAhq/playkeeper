import { useEffect, useState } from 'react'
import { get } from '@/api/client'
import type { NameAvailability } from '@/api/types'
import { machineApi } from '@/api/workspace'
import { nameProblem, normalizeName } from '@/lib/address'

// Names offered when a free name is wanted and the obvious one can't be had,
// like "admin", which the names service reserves: an adjective, an animal
// and a number, within the service's rule (3–32 of a–z, 0–9 and single
// dashes) and clear of its reserved words.

const adjectives = ['brave', 'bright', 'calm', 'clever', 'cosy', 'eager', 'gentle', 'happy', 'jolly', 'lucky', 'merry', 'mighty', 'nimble', 'plucky', 'quick', 'quiet', 'snowy', 'sunny', 'swift', 'witty', 'bold', 'cheery', 'fuzzy', 'golden', 'mossy']
const animals = ['otter', 'fox', 'panda', 'axolotl', 'ocelot', 'parrot', 'turtle', 'dolphin', 'llama', 'bee', 'frog', 'goat', 'wolf', 'owl', 'badger', 'beaver', 'rabbit', 'falcon', 'heron', 'lynx', 'moose', 'puffin', 'raven', 'seal', 'yak']

/** A friendly name like "brave-otter-17". */
export function friendlyName(random: () => number = Math.random): string {
  const pick = (words: string[]) => words[Math.floor(random() * words.length)] ?? words[0]
  return `${pick(adjectives)}-${pick(animals)}-${10 + Math.floor(random() * 90)}`
}

/** The free name someone starts with: their username, when it can be one. */
export function startingName(username: string, base: string): string {
  const n = normalizeName(username, base)
  return nameProblem(n) ? '' : n
}

/** A source of numbers in [0, 1) that gives the same ones for the same seed. */
export function seeded(seed: string): () => number {
  let h = 2166136261
  for (let i = 0; i < seed.length; i++) h = Math.imul(h ^ seed.charCodeAt(i), 16777619)
  return () => {
    h = (h + 0x6d2b79f5) | 0
    let x = Math.imul(h ^ (h >>> 15), h | 1)
    x ^= x + Math.imul(x ^ (x >>> 7), x | 61)
    return ((x ^ (x >>> 14)) >>> 0) / 4294967296
  }
}

/**
 * A free name to offer: first, when the names service says it's free, else
 * the first free name it suggests for it, else friendly names, each checked,
 * so a reserved name like "admin" is never offered. Undefined while it
 * looks or when off, null when the service can't be asked. The friendly
 * names are the same for the same machine and round, so a page offers the
 * same name each time it opens; a round after the first starts from them.
 */
export function useFreeSuggestion(id: string, first: string, { round = 0, off = false }: { round?: number; off?: boolean } = {}): string | null | undefined {
  const [found, setFound] = useState<{ key: string; name: string | null }>()
  const key = `${id}\n${first}\n${round}`
  const known = found?.key === key
  useEffect(() => {
    if (off || known) return
    let stopped = false
    const look = (n: string) => get<NameAvailability>(machineApi(id, `/address/available?name=${encodeURIComponent(n)}`))
    void (async () => {
      const friendly = Array.from({ length: 4 }, (_, i) => friendlyName(seeded(`${id}\n${round}\n${i}`)))
      const candidates = [...(round === 0 && first && !nameProblem(first) ? [first] : []), ...friendly]
      for (const n of candidates) {
        let av: NameAvailability
        try {
          av = await look(n)
        } catch {
          if (!stopped) setFound({ key, name: null })
          return
        }
        if (stopped) return
        const pick = av.available ? n : av.suggestions?.[0]
        if (pick) {
          setFound({ key, name: pick })
          return
        }
      }
      if (!stopped) setFound({ key, name: null })
    })()
    return () => {
      stopped = true
    }
  }, [id, first, round, off, known, key])
  return !off && known ? found.name : undefined
}
