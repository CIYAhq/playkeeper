import { useCallback, useState } from 'react'

// Optimistic updates are only for small changes that are safe to show before
// the server confirms them and easy to take back if it refuses. Deleting,
// restoring, updating and anything else long or destructive waits instead.

/** Preferences after a change; an empty value removes the key, as the panel does. */
export function mergePrefs(prefs: Record<string, string>, change: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries({ ...prefs, ...change }).filter(([, value]) => value !== ''))
}

/** The change that puts back what `change` replaced in `before`. */
export function undoPrefs(before: Record<string, string>, change: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.keys(change).map((key) => [key, before[key] ?? '']))
}

/** A row being added to or removed (by key) from a list before the server confirms it. */
export type ListChange<T> = { add: T } | { remove: string }

/** A list as it will be once `changes` are saved; a list that isn't loaded stays that way. */
export function withChanges<T>(list: T[] | undefined, changes: readonly ListChange<T>[], keyOf: (item: T) => string): T[] | undefined {
  if (!list) return undefined
  return changes.reduce<T[]>((out, c) => {
    if ('remove' in c) return out.filter((item) => keyOf(item) !== c.remove)
    const key = keyOf(c.add)
    return out.some((item) => keyOf(item) === key) ? out : [...out, c.add]
  }, list)
}

export interface Pending<C> {
  /** Changes still being saved, oldest first. */
  changes: readonly C[]
  /**
   * Shows `change` at once and saves it. A saved change stays shown until
   * `reload` has fetched data that includes it; a change that fails to save
   * disappears again and `run` throws the error for the caller to explain.
   */
  run: (change: C, save: () => Promise<unknown>, reload: () => Promise<void>) => Promise<void>
}

export function usePending<C>(): Pending<C> {
  const [changes, setChanges] = useState<readonly C[]>([])
  const run = useCallback(async (change: C, save: () => Promise<unknown>, reload: () => Promise<void>) => {
    setChanges((cs) => [...cs, change])
    try {
      await save()
      await reload()
    } finally {
      setChanges((cs) => cs.filter((c) => c !== change))
    }
  }, [])
  return { changes, run }
}
