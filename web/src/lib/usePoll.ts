import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError } from '../api/client'

export interface Poll<T> {
  data: T | undefined
  error: ApiError | undefined
  loading: boolean
  refresh: () => Promise<void>
}

/**
 * Fetches immediately and then every `intervalMs` while the tab is visible.
 * On error the previous data is dropped, so stale values are never shown as
 * current.
 */
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number, key = ''): Poll<T> {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<ApiError>()
  const [loading, setLoading] = useState(true)
  const fetchRef = useRef(fetcher)
  useEffect(() => {
    fetchRef.current = fetcher
  })

  const refresh = useCallback(async () => {
    try {
      const d = await fetchRef.current()
      setData(d)
      setError(undefined)
    } catch (e) {
      setData(undefined)
      setError(e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' }))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let stopped = false
    setLoading(true)
    void refresh()
    const id = window.setInterval(() => {
      if (!stopped && document.visibilityState === 'visible') void refresh()
    }, intervalMs)
    return () => {
      stopped = true
      window.clearInterval(id)
    }
  }, [refresh, intervalMs, key])

  return { data, error, loading, refresh }
}
