import { useEffect, useState } from 'react'
import { get } from './client'
import type { ModpackCard, ModpackDetail, ModpackPreview, ModpackResults, ModpackSource } from './types'
import { errorText, machineApi } from './workspace'

export type ModpackSort = 'downloads' | 'relevance' | 'updated' | 'newest'

const cache = new Map<string, { at: number; data: unknown }>()
const maxAge = 5 * 60_000

/** GETs path once per five minutes, or each time with fresh; an empty path loads nothing. */
function useCached<T>(path: string, fresh = false) {
  const [data, setData] = useState<{ path: string; value?: T; error?: string }>({ path: '' })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!path) return
    const hit = cache.get(path)
    if (hit && !fresh && Date.now() - hit.at < maxAge && tick === 0) {
      setData({ path, value: hit.data as T })
      return
    }
    let cancelled = false
    setData({ path })
    get<T>(path)
      .then((v) => {
        cache.set(path, { at: Date.now(), data: v })
        if (!cancelled) setData({ path, value: v })
      })
      .catch((e: unknown) => !cancelled && setData({ path, error: errorText(e) }))
    return () => {
      cancelled = true
    }
  }, [path, tick, fresh])
  const current = data.path === path ? data : { path, value: undefined, error: undefined }
  return { data: current.value, error: current.error, loading: !!path && !current.value && !current.error, reload: () => setTick((n) => n + 1) }
}

/** One page of packs from a source, most downloaded first unless sorted otherwise. */
export function useModpacks(machineId: string | undefined, q: string, sort: ModpackSort, source: ModpackSource = 'modrinth') {
  // The library drops packs a server can't run, so ask for more than the four shown.
  const params = new URLSearchParams({ source, sort, limit: '12' })
  if (q.trim()) params.set('q', q.trim())
  return useCached<ModpackResults>(machineId ? machineApi(machineId, `/modpacks?${params.toString()}`) : '')
}

/**
 * The create flow's packs: Modrinth's, and CurseForge's as well once the
 * machine offers CurseForge, which every answer says in `sources`. CurseForge
 * is asked only then, as it answers an error without a key. While one source
 * fails the other's packs still show, with its error in `unanswered`.
 */
export function usePackLibrary(machineId: string | undefined, q: string, sort: ModpackSort) {
  const modrinth = useModpacks(machineId, q, sort)
  const [offered, setOffered] = useState<{ machineId: string; sources: ModpackSource[] }>()
  useEffect(() => {
    if (machineId && modrinth.data) setOffered({ machineId, sources: modrinth.data.sources })
  }, [machineId, modrinth.data])
  const sources = modrinth.data?.sources ?? (offered && offered.machineId === machineId ? offered.sources : undefined)
  const curseforge = useModpacks(sources?.includes('curseforge') ? machineId : undefined, q, sort, 'curseforge')
  const lists = sources?.includes('curseforge') ? [modrinth, curseforge] : [modrinth]
  const loading = lists.some((l) => l.loading)
  const answered = lists.flatMap((l) => (l.data ? [l.data.cards] : []))
  const failed = lists.flatMap((l) => (l.error ? [l.error] : []))
  return {
    cards: loading || answered.length === 0 ? undefined : mergePacks(answered, sort),
    sources,
    loading,
    error: loading || answered.length > 0 ? undefined : failed[0],
    unanswered: loading || answered.length === 0 ? [] : failed,
    reload: () => lists.forEach((l) => l.reload()),
  }
}

/**
 * Both sources' packs in one list, as the add-on library merges its sources:
 * relevance takes the sources in turns, and so does newest, as a card doesn't
 * say when its pack was first published; the other orders sort the lot.
 */
export function mergePacks(lists: ModpackCard[][], sort: ModpackSort): ModpackCard[] {
  const all: ModpackCard[] = []
  for (let i = 0; lists.some((l) => i < l.length); i++) {
    for (const l of lists) {
      const c = l[i]
      if (c) all.push(c)
    }
  }
  switch (sort) {
    case 'downloads':
      return all.sort((a, b) => b.downloads - a.downloads)
    case 'updated':
      return all.sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated))
    case 'relevance':
    case 'newest':
      return all
    default: {
      const unreachable: never = sort
      return unreachable
    }
  }
}

export function useModpackDetail(machineId: string | undefined, source: ModpackSource | undefined, project: string | undefined) {
  return useCached<ModpackDetail>(machineId && source && project ? machineApi(machineId, `/modpacks/${source}/${encodeURIComponent(project)}`) : '')
}

/**
 * What a pack version needs and puts on the server, read from the pack itself.
 * It's asked for each time: the agent keeps the pack's plan, but works out the
 * ports its add-ons would get as other servers take them.
 */
export function useModpackPreview(machineId: string | undefined, source: ModpackSource | undefined, project: string | undefined, version: string | undefined) {
  return useCached<ModpackPreview>(machineId && source && project && version ? machineApi(machineId, `/modpacks/${source}/${encodeURIComponent(project)}/versions/${encodeURIComponent(version)}/preview`) : '', true)
}

/** The dashboard's copy of a pack's icon from Modrinth's file host. */
export function modpackIcon(machineId: string, url: string): string {
  return machineApi(machineId, `/modpacks/icon?${new URLSearchParams({ url }).toString()}`)
}
