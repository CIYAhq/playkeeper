import type { Params } from '@/api/types'

/** A finite number from a diagnosis's params. */
export function num(p: Params | undefined, key: string): number | undefined {
  const v = p?.[key]
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

/** A non-blank string from a diagnosis's params. */
export function str(p: Params | undefined, key: string): string | undefined {
  const v = p?.[key]
  return typeof v === 'string' && v.trim() !== '' ? v : undefined
}

/** The non-blank strings of a list in a diagnosis's params. */
export function strs(p: Params | undefined, key: string): string[] {
  const v = p?.[key]
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x.trim() !== '') : []
}
