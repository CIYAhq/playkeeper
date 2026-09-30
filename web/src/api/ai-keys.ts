import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, del, get, put } from './client'
import type { AIKeys, AIProvider } from './types'
import { serverApi } from './workspace'

export interface AIKeysState {
  /** Whether each key is set and applies yet, never a key; undefined while off, loading, or after an error. */
  keys: AIKeys | undefined
  error: ApiError | undefined
  /** Saves key for the provider in place of the one it had, and answers with the new state. */
  save: (provider: AIProvider, key: string) => Promise<AIKeys>
  remove: (provider: AIProvider) => Promise<AIKeys>
}

/**
 * A server's AI keys, for an account that may change its files: asked for
 * only while on, again every minute, and whenever the server's phase changes,
 * since a restart applies a key that waits for one.
 */
export function useAIKeys(serverId: string, on: boolean, phase?: string): AIKeysState {
  const [state, setState] = useState<{ for: string; keys?: AIKeys; error?: ApiError }>()
  // A read that set off before a save or removal would answer with the old state.
  const writes = useRef(0)
  useEffect(() => {
    if (!on) return
    let stale = false
    const ask = () => {
      const before = writes.current
      const fresh = () => !stale && before === writes.current
      get<AIKeys>(serverApi(serverId, '/ai-keys')).then(
        (keys) => fresh() && setState({ for: serverId, keys }),
        (e: unknown) => fresh() && setState({ for: serverId, error: e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' }) }),
      )
    }
    ask()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') ask()
    }, 60_000)
    return () => {
      stale = true
      window.clearInterval(timer)
    }
  }, [serverId, on, phase])

  const save = useCallback(
    async (provider: AIProvider, key: string) => {
      writes.current++
      const keys = await put<AIKeys>(serverApi(serverId, `/ai-keys/${provider}`), { key })
      setState({ for: serverId, keys })
      return keys
    },
    [serverId],
  )
  const remove = useCallback(
    async (provider: AIProvider) => {
      writes.current++
      const keys = await del<AIKeys>(serverApi(serverId, `/ai-keys/${provider}`))
      setState({ for: serverId, keys })
      return keys
    },
    [serverId],
  )
  const mine = on && state?.for === serverId ? state : undefined
  return { keys: mine?.keys, error: mine?.error, save, remove }
}
