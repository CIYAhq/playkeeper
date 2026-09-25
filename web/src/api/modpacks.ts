import { useEffect, useState } from 'react'
import { get } from './client'
import type { ModpackDetail, ModpackPreview, ModpackResults, ModpackSource } from './types'
import { errorText, machineApi } from './workspace'

export type ModpackSort = 'downloads' | 'relevance' | 'updated' | 'newest'

const cache = new Map<string, { at: number; data: unknown }>()
const maxAge = 5 * 60_000

/** GETs path once per five minutes; an empty path loads nothing. */
function useCached<T>(path: string) {
  const [data, setData] = useState<{ path: string; value?: T; error?: string }>({ path: '' })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!path) return
    const hit = cache.get(path)
    if (hit && Date.now() - hit.at < maxAge && tick === 0) {
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
  }, [path, tick])
  const current = data.path === path ? data : { path, value: undefined, error: undefined }
  return { data: current.value, error: current.error, loading: !!path && !current.value && !current.error, reload: () => setTick((n) => n + 1) }
}

/** One page of packs from a source, most downloaded first unless sorted otherwise. */
export function useModpacks(machineId: string | undefined, q: string, sort: ModpackSort, source: ModpackSource = 'modrinth') {
  const params = new URLSearchParams({ source, sort })
  if (q.trim()) params.set('q', q.trim())
  return useCached<ModpackResults>(machineId ? machineApi(machineId, `/modpacks?${params.toString()}`) : '')
}

export function useModpackDetail(machineId: string | undefined, source: ModpackSource | undefined, project: string | undefined) {
  return useCached<ModpackDetail>(machineId && source && project ? machineApi(machineId, `/modpacks/${source}/${encodeURIComponent(project)}`) : '')
}

/** What a pack version needs and puts on the server, read from the pack itself. */
export function useModpackPreview(machineId: string | undefined, source: ModpackSource | undefined, project: string | undefined, version: string | undefined) {
  return useCached<ModpackPreview>(machineId && source && project && version ? machineApi(machineId, `/modpacks/${source}/${encodeURIComponent(project)}/versions/${encodeURIComponent(version)}/preview`) : '')
}

/** The dashboard's copy of a pack's icon from Modrinth's file host. */
export function modpackIcon(machineId: string, url: string): string {
  return machineApi(machineId, `/modpacks/icon?${new URLSearchParams({ url }).toString()}`)
}
