import { useEffect, useLayoutEffect, useState, type ReactNode } from 'react'

export type Presence = 'entering' | 'staying' | 'leaving'

export interface Present<T> {
  key: string
  item: T
  state: Presence
}

/**
 * Merges a list's new items into the rows on screen. New keys enter (unless
 * this is the list's first data), keys that went away stay where they were,
 * marked leaving, so they can fade out; everything else follows `next`.
 */
export function presence<T>(rows: Present<T>[], next: T[], keyOf: (item: T) => string, first = false): Present<T>[] {
  const before = new Map(rows.map((r) => [r.key, r]))
  const out: Present<T>[] = next.map((item) => {
    const key = keyOf(item)
    const was = before.get(key)
    const state: Presence = first ? 'staying' : !was || was.state === 'leaving' ? 'entering' : was.state
    return { key, item, state }
  })
  const kept = new Set(out.map((r) => r.key))
  rows.forEach((r, i) => {
    if (kept.has(r.key)) return
    const after = rows
      .slice(0, i)
      .reverse()
      .find((p) => kept.has(p.key))
    out.splice(after ? out.findIndex((o) => o.key === after.key) + 1 : 0, 0, { ...r, state: 'leaving' })
    kept.add(r.key)
  })
  return out
}

/** Settles rows once their enter or leave transition is over. */
export function settle<T>(rows: Present<T>[]): Present<T>[] {
  return rows.filter((r) => r.state !== 'leaving').map((r) => (r.state === 'entering' ? { ...r, state: 'staying' } : r))
}

/** How long rows take to enter or leave: the standard motion token, or nothing with reduced motion. */
function transitionMs(): number {
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return 0
  const token = getComputedStyle(document.documentElement).getPropertyValue('--motion-standard')
  return Number.parseFloat(token) || 200
}

function sameItems<T>(a: T[] | undefined, b: T[] | undefined): boolean {
  if (a === b) return true
  return !!a && !!b && a.length === b.length && a.every((item, i) => item === b[i])
}

/**
 * A list's rows with their enter/leave state, for `data-entering` and
 * `data-leaving` (styles.css animates both). Undefined items mean the list
 * isn't loaded, so its first data shows without animating every row. Items
 * are compared one by one, so a list rebuilt from the same items each render
 * is fine.
 */
export function useListPresence<T>(items: T[] | undefined, keyOf: (item: T) => string): Present<T>[] {
  const [seen, setSeen] = useState(items)
  const [rows, setRows] = useState<Present<T>[]>(() => presence([], items ?? [], keyOf, true))
  if (!sameItems(items, seen)) {
    setSeen(items)
    setRows(items ? presence(rows, items, keyOf, seen === undefined) : [])
  }
  const moving = rows.some((r) => r.state !== 'staying')
  useEffect(() => {
    if (!moving) return
    const id = window.setTimeout(() => setRows(settle), transitionMs())
    return () => window.clearTimeout(id)
  }, [moving, rows])
  return rows
}

/** The attributes a row needs for its presence: enter and leave styles, and no interaction while it leaves. */
export function presenceProps(state: Presence) {
  return {
    'data-entering': state === 'entering' ? '' : undefined,
    'data-leaving': state === 'leaving' ? '' : undefined,
    inert: state === 'leaving' || undefined,
  }
}

/** The keyframes things come in with: animate-fade, animate-enter and rows joining a list. */
const entrances = new Set(['fade', 'enter'])

/**
 * Puts whatever is fading or sliding in right now straight in place. A new
 * page, tab or step of a flow calls it as it commits, so it shows at once with
 * everything on it; what changes after that, like a status or a row joining a
 * list, still animates.
 */
export function appearAtOnce() {
  for (const a of document.getAnimations?.() ?? []) {
    const name = (a as Partial<CSSAnimation>).animationName
    if (name && entrances.has(name) && a.effect?.getComputedTiming().endTime !== Infinity) a.finish()
  }
}

/** Calls appearAtOnce whenever `key` changes, before the browser paints. */
export function useAppearAtOnce(key: unknown) {
  useLayoutEffect(() => appearAtOnce(), [key])
}

/** Content that may arrive after its page or tab opened, like a page whose code was still loading, shows at once too. */
export function Appear({ children }: { children: ReactNode }) {
  useLayoutEffect(() => appearAtOnce(), [])
  return children
}
