import { useEffect, useRef, useState } from 'react'
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

/**
 * A free name to offer: first, when the names service says it's free, else
 * the first of the free names it suggests for it, else friendly names, each
 * checked, so a reserved name like "admin" is never offered. Undefined while
 * it looks or when off, null when the service can't be asked. Each round
 * after the first skips first and every name it offered before.
 */
export function useFreeSuggestion(id: string, first: string, { round = 0, off = false }: { round?: number; off?: boolean } = {}): string | null | undefined {
  const [found, setFound] = useState<{ key: string; name: string | null }>()
  const offered = useRef<string[]>([])
  const key = `${id}\n${first}\n${round}`
  useEffect(() => {
    if (off) return
    let stopped = false
    const look = (n: string) => get<NameAvailability>(machineApi(id, `/address/available?name=${encodeURIComponent(n)}`))
    void (async () => {
      const candidates = [...(round === 0 && first && !nameProblem(first) ? [first] : []), ...Array.from({ length: 4 }, () => friendlyName())]
      for (const n of candidates) {
        if (offered.current.includes(n)) continue
        let av: NameAvailability
        try {
          av = await look(n)
        } catch {
          if (!stopped) setFound({ key, name: null })
          return
        }
        if (stopped) return
        const pick = av.available ? n : av.suggestions?.find((s) => !offered.current.includes(s))
        if (pick) {
          offered.current = [...offered.current, pick]
          setFound({ key, name: pick })
          return
        }
      }
      if (!stopped) setFound({ key, name: null })
    })()
    return () => {
      stopped = true
    }
  }, [id, first, round, off, key])
  return !off && found?.key === key ? found.name : undefined
}
