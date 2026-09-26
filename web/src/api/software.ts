import { useEffect, useState } from 'react'
import { get } from './client'
import type { SoftwareBuilds } from './types'
import { errorText, machineApi } from './workspace'

const cache = new Map<string, { at: number; data: SoftwareBuilds }>()
const maxAge = 5 * 60_000

/** A type's builds for one Minecraft version: Purpur builds, loaders, or NeoForge or Forge versions. */
export function useBuilds(machineId: string | undefined, type: string, version: string | undefined) {
  const path = machineId && version ? machineApi(machineId, `/catalog/builds?${new URLSearchParams({ type, version }).toString()}`) : ''
  const [data, setData] = useState<{ path: string; builds?: SoftwareBuilds; error?: string }>({ path: '' })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!path) return
    const hit = cache.get(path)
    if (hit && Date.now() - hit.at < maxAge && tick === 0) {
      setData({ path, builds: hit.data })
      return
    }
    let cancelled = false
    setData({ path })
    get<SoftwareBuilds>(path)
      .then((b) => {
        cache.set(path, { at: Date.now(), data: b })
        if (!cancelled) setData({ path, builds: b })
      })
      .catch((e: unknown) => !cancelled && setData({ path, error: errorText(e) }))
    return () => {
      cancelled = true
    }
  }, [path, tick])
  const current = data.path === path ? data : { path }
  return { builds: current.builds, error: current.error, loading: !!path && !current.builds && !current.error, reload: () => setTick((n) => n + 1) }
}
