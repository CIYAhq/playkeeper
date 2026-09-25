import { useEffect, useState } from 'react'
import { get } from './client'
import type { Catalog } from './types'
import { machineApi } from './workspace'

// PaperMC's version list changes rarely; pages share one copy for a while.
const cache = new Map<string, { at: number; data: Catalog }>()
const maxAge = 5 * 60_000

export function catalogPath(machineId: string, opts: { server?: string; type?: string } = {}): string {
  const q = new URLSearchParams()
  if (opts.server) q.set('server', opts.server)
  if (opts.type) q.set('type', opts.type)
  const qs = q.toString()
  return machineApi(machineId, `/catalog${qs ? `?${qs}` : ''}`)
}

/** The machine's catalog: server types, Minecraft versions and memory options. */
export function useCatalog(machineId: string | undefined, opts: { server?: string; type?: string; fresh?: boolean } = {}) {
  const path = machineId ? catalogPath(machineId, opts) : ''
  const cached = path ? cache.get(path) : undefined
  const [data, setData] = useState<Catalog | undefined>(cached?.data)
  const [error, setError] = useState<string>()
  const [tick, setTick] = useState(0)
  const fresh = opts.fresh
  useEffect(() => {
    if (!path) return
    const hit = cache.get(path)
    if (hit && !fresh && Date.now() - hit.at < maxAge && tick === 0) {
      setData(hit.data)
      return
    }
    let cancelled = false
    get<Catalog>(path)
      .then((c) => {
        cache.set(path, { at: Date.now(), data: c })
        if (!cancelled) {
          setData(c)
          setError(undefined)
        }
      })
      .catch((e: unknown) => !cancelled && setError(e instanceof Error ? e.message : String(e)))
    return () => {
      cancelled = true
    }
  }, [path, fresh, tick])
  return { catalog: data, error, reload: () => setTick((n) => n + 1) }
}
