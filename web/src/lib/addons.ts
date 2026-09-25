import type { Addon, AddonCard, AddonChecks, AddonDetails, AddonKey, AddonNotice, AddonProgress, AddonSource, AddonStep, AddonVersion, Addons, Operation } from '@/api/types'
import { formatLocale } from '@/i18n'
import { relativeTime } from '@/lib/format'

export type AddonKind = 'plugin' | 'mod'

const kinds: Record<string, AddonKind> = { paper: 'plugin', purpur: 'plugin', fabric: 'mod', quilt: 'mod', neoforge: 'mod' }

/** Whether a server type runs plugins or mods; undefined for types with neither, like Vanilla. */
export function addonKind(serverType: string | undefined): AddonKind | undefined {
  return serverType ? kinds[serverType] : undefined
}

/** The server's add-on tab: Plugins or Mods, or none. */
export function addonTab(serverType: string | undefined): 'plugins' | 'mods' | undefined {
  const kind = addonKind(serverType)
  if (!kind) return undefined
  return kind === 'plugin' ? 'plugins' : 'mods'
}

/** The libraries' own names, which aren't translated. */
export const sourceNames: Record<AddonSource, string> = { modrinth: 'Modrinth', hangar: 'Hangar' }

export const keyOf = (a: AddonKey): string => `${a.source}:${a.projectId}`
export const sameKey = (a: AddonKey, b: AddonKey): boolean => a.source === b.source && a.projectId === b.projectId
/** Just the key: the agent refuses fields it doesn't know. */
export const keyFrom = (a: AddonKey): AddonKey => ({ source: a.source, projectId: a.projectId })

/**
 * managed: installed by Playkeeper and unchanged. changed: installed by
 * Playkeeper, then edited. missing: installed by Playkeeper, file gone.
 * identified: added by hand, and Modrinth knows it. unknown: added by hand.
 */
export type RowState = 'managed' | 'changed' | 'missing' | 'identified' | 'unknown'

export interface AddonRow {
  id: string
  state: RowState
  name: string
  version: string
  /** Playkeeper's record, or what Modrinth knows a file added by hand as. */
  addon?: Addon
  fileName: string
  /** Put in place after the running server started: it loads at the next restart. */
  pending: boolean
  update?: AddonVersion
}

/**
 * The installed list: every file in the folder, then every record whose file
 * is gone, with the updates and the files Modrinth recognised from the
 * checks, sorted by name.
 */
export function mergeRows(addons: Addons, checks?: AddonChecks): AddonRow[] {
  const updates = new Map<string, AddonVersion>()
  for (const u of checks?.updates ?? []) if (u.available && u.latest) updates.set(keyOf(u), u.latest)
  const identified = new Map<string, Addon>()
  for (const f of checks?.identified ?? []) if (f.addon) identified.set(f.fileName, f.addon)

  const rows: AddonRow[] = []
  for (const f of addons.files) {
    const known = f.status === 'unknown' ? identified.get(f.fileName) : f.addon
    if ((f.status === 'managed' || f.status === 'modified') && f.addon) {
      rows.push({ id: keyOf(f.addon), state: f.status === 'managed' ? 'managed' : 'changed', name: f.addon.name, version: f.addon.versionNumber, addon: f.addon, fileName: f.fileName, pending: !!f.pending, update: updates.get(keyOf(f.addon)) })
    } else if (known) {
      rows.push({ id: `file:${f.fileName}`, state: 'identified', name: known.name, version: known.versionNumber, addon: known, fileName: f.fileName, pending: false })
    } else {
      rows.push({ id: `file:${f.fileName}`, state: 'unknown', name: f.name || f.fileName.replace(/\.jar$/i, ''), version: f.version ?? '', fileName: f.fileName, pending: false })
    }
  }
  for (const a of addons.missing) {
    rows.push({ id: keyOf(a), state: 'missing', name: a.name, version: a.versionNumber, addon: a, fileName: a.fileName, pending: false })
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name, formatLocale(), { sensitivity: 'base' }) || a.fileName.localeCompare(b.fileName))
}

/** Rows "Update all" updates: files that changed since they were installed wait to be asked one by one. */
export function updatableRows(rows: AddonRow[]): AddonRow[] {
  return rows.filter((r) => r.state === 'managed' && r.update && r.addon)
}

export function updateKeys(rows: AddonRow[]): AddonKey[] {
  return updatableRows(rows).flatMap((r) => (r.addon ? [keyFrom(r.addon)] : []))
}

export function pendingCount(rows: AddonRow[]): number {
  return rows.filter((r) => r.pending).length
}

/** What the detail sheet's pinned footer offers. */
export type Footer =
  | { kind: 'install'; fingerprint: string }
  | { kind: 'external'; url?: string }
  | { kind: 'conflict'; other: string; file: string }
  | { kind: 'needs'; dependency: string; url?: string }
  | { kind: 'installed'; update?: AddonVersion; changed: boolean; missing: boolean }
  | { kind: 'blocked'; notice?: AddonNotice }

export function footerFor(d: AddonDetails): Footer {
  if (d.installed) return { kind: 'installed', update: d.updateAvailable ? d.latest : undefined, changed: !!d.changed, missing: !!d.missing }
  if (d.latest?.externalUrl) return { kind: 'external', url: d.latest.externalUrl }
  const p = d.plan
  if (p) {
    // A conflict with an installed file names that file; one between two
    // add-ons of the plan does not, and has nowhere to go.
    const conflict = p.blockers.find((b) => b.kind === 'conflict' && b.params?.other && b.params.file)
    if (conflict?.params) return { kind: 'conflict', other: conflict.params.other ?? '', file: conflict.params.file ?? '' }
    const dep = p.manual.find((m) => m.kind === 'dependency_external' || m.kind === 'dependency_unlisted')
    if (dep) return { kind: 'needs', dependency: dep.params?.dependency ?? dep.params?.file ?? '', url: dep.url }
    const external = p.manual.find((m) => m.kind === 'external_download')
    if (external) return { kind: 'external', url: external.url }
    if (p.ready) return { kind: 'install', fingerprint: p.fingerprint }
    return { kind: 'blocked', notice: p.blockers[0] ?? p.manual[0] }
  }
  return { kind: 'blocked', notice: d.planError ?? d.notice }
}

/** The dependencies an install brings along. */
export function alsoInstalls(d: AddonDetails): AddonStep[] {
  return (d.plan?.steps ?? []).filter((s) => !sameKey(s, d.card))
}

/** The page with an add-on version's notes, next to its project page. */
export function versionPage(card: AddonCard, v: AddonVersion): string | undefined {
  if (!card.pageUrl) return undefined
  const base = card.pageUrl.replace(/\/+$/, '')
  return card.source === 'modrinth' ? `${base}/version/${encodeURIComponent(v.versionId)}` : `${base}/versions/${encodeURIComponent(v.versionNumber)}`
}

export type BrowseSort = 'downloads' | 'relevance' | 'updated'
export const browseSorts: BrowseSort[] = ['downloads', 'relevance', 'updated']

export interface BrowseFilter {
  q: string
  category: string
  sort: BrowseSort
}

export const maxSearch = 100

/** The library search for one page, as the agent reads it. */
export function searchPath(serverId: string, f: BrowseFilter, page = 0): string {
  const p = new URLSearchParams()
  const q = [...f.q.trim()].slice(0, maxSearch).join('')
  if (q) p.set('q', q)
  if (f.category) p.set('category', f.category)
  if (f.sort !== 'downloads') p.set('sort', f.sort)
  if (page > 0) p.set('page', String(page))
  const qs = p.toString()
  return `/api/servers/${serverId}/addons/search${qs ? `?${qs}` : ''}`
}

const norm = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '')

/**
 * Adds the next page of results. The agent merges each page's sources; an
 * add-on can still come back on a later page from its other listing, so it
 * is dropped by project and by name.
 */
export function appendCards(prev: AddonCard[], next: AddonCard[]): AddonCard[] {
  const keys = new Set(prev.map(keyOf))
  const names = new Set(prev.map((c) => norm(c.name)).filter(Boolean))
  const out = [...prev]
  for (const c of next) {
    const n = norm(c.name)
    if (keys.has(keyOf(c)) || (n && names.has(n))) continue
    keys.add(keyOf(c))
    if (n) names.add(n)
    out.push(c)
  }
  return out
}

export function isAddonOp(op: Operation | undefined): op is Operation {
  return op?.kind === 'addon-install' || op?.kind === 'addon-update'
}

export function opFiles(op: Operation | undefined): AddonProgress[] {
  const files = op?.detail?.files
  return Array.isArray(files) ? (files as AddonProgress[]) : []
}

export function opNotice(op: Operation | undefined): AddonNotice | undefined {
  const n = op?.detail?.notice
  return n && typeof n === 'object' && typeof (n as AddonNotice).kind === 'string' ? (n as AddonNotice) : undefined
}

export function opRestartNeeded(op: Operation | undefined): boolean {
  return op?.detail?.restartNeeded === true
}

/** Whether a failed download failed its checksum, which the dialog words itself. */
export function checksumFailed(n: AddonNotice | undefined): boolean {
  return n?.kind === 'hash_mismatch' || n?.kind === 'size_mismatch'
}

/** "5 days ago", "2 weeks ago", "3 years ago": when a project last changed. */
export function updatedAgo(iso: string, now: number = Date.now()): string {
  const days = Math.floor((now - new Date(iso).getTime()) / 86_400_000)
  if (!Number.isFinite(days)) return ''
  if (days < 7) return relativeTime(iso, now)
  const rtf = new Intl.RelativeTimeFormat(formatLocale(), { numeric: 'always' })
  if (days < 30) return rtf.format(-Math.floor(days / 7), 'week')
  if (days < 365) return rtf.format(-Math.floor(days / 30), 'month')
  return rtf.format(-Math.floor(days / 365), 'year')
}

/** "2.1M", "380K": downloads as the library cards show them. */
export function compactCount(n: number): string {
  return new Intl.NumberFormat(formatLocale(), { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}
