import { useCallback, useEffect, useState } from 'react'
import { ApiError, get, post } from './client'
import type { PackPage, PackShare } from './types'
import { errorText, serverApi } from './workspace'

/** Where a link token's page lives on this dashboard's address. */
export function packLink(token: string): string {
  return `${window.location.origin}/packs/${token}`
}

/** The friends' file of a server, for a signed-in user; it works without sharing. */
export function packFileUrl(serverId: string): string {
  return serverApi(serverId, '/mods/share.mrpack')
}

type ShareState = { id?: string; share?: PackShare; error?: string; unsupported?: boolean }

// The Mods tab asks for a server's share from several places as it opens;
// asks made together wait for the same answer, and a later one asks again.
const asking = new Map<string, Promise<ShareState>>()

function askShare(serverId: string): Promise<ShareState> {
  let p = asking.get(serverId)
  if (!p) {
    const ask = get<PackShare>(serverApi(serverId, '/mods/share'))
      .then((share): ShareState => ({ id: serverId, share }))
      .catch((e: unknown): ShareState => ({ id: serverId, error: errorText(e), unsupported: e instanceof ApiError && e.status === 409 }))
    asking.set(serverId, ask)
    window.setTimeout(() => asking.get(serverId) === ask && asking.delete(serverId), 0)
    p = ask
  }
  return p
}

/** A server's friends' pack, and whether its page is shared. */
export function usePackShare(serverId: string | undefined) {
  const [data, setData] = useState<ShareState>({})
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!serverId) return
    let cancelled = false
    void askShare(serverId).then((s) => !cancelled && setData(s))
    return () => {
      cancelled = true
    }
  }, [serverId, tick])
  const setPublic = useCallback(
    async (on: boolean) => {
      if (!serverId) return
      const share = await post<PackShare>(serverApi(serverId, '/mods/share'), { public: on })
      setData({ id: serverId, share })
    },
    [serverId],
  )
  const current = data.id === serverId ? data : {}
  return {
    share: current.share,
    error: current.error,
    // The server's type runs no mods, so there is no pack to share.
    unsupported: !!current.unsupported,
    loading: !!serverId && !current.share && !current.error,
    reload: () => {
      setData({})
      setTick((n) => n + 1)
    },
    setPublic,
  }
}

export type PackPageState = { kind: 'loading' } | { kind: 'ready'; page: PackPage } | { kind: 'gone' } | { kind: 'busy' }

/**
 * The public page's data. The panel answers every link that doesn't open a
 * shared pack with one 404, so all of them are "gone"; "busy" is the panel
 * turning away too many requests, or no answer at all.
 */
export function usePackPage(token: string) {
  const [state, setState] = useState<{ token: string; value: PackPageState }>({ token, value: { kind: 'loading' } })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!token) return
    let cancelled = false
    fetch(`/packs/${token}/page`, { credentials: 'omit', cache: 'no-store' })
      .then(async (res): Promise<PackPageState> => {
        if (res.ok) return { kind: 'ready', page: (await res.json()) as PackPage }
        return res.status === 429 || res.status === 503 ? { kind: 'busy' } : { kind: 'gone' }
      })
      .catch((): PackPageState => ({ kind: 'busy' }))
      .then((value) => !cancelled && setState({ token, value }))
    return () => {
      cancelled = true
    }
  }, [token, tick])
  const retry = () => {
    setState({ token, value: { kind: 'loading' } })
    setTick((n) => n + 1)
  }
  if (!token) return { state: { kind: 'gone' } as PackPageState, retry }
  return { state: state.token === token ? state.value : ({ kind: 'loading' } as PackPageState), retry }
}
