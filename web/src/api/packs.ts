import { useCallback, useEffect, useState } from 'react'
import { get, post } from './client'
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

/** A server's friends' pack, and whether its page is shared. */
export function usePackShare(serverId: string | undefined) {
  const [data, setData] = useState<{ id?: string; share?: PackShare; error?: string }>({})
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!serverId) return
    let cancelled = false
    get<PackShare>(serverApi(serverId, '/mods/share'))
      .then((share) => !cancelled && setData({ id: serverId, share }))
      .catch((e: unknown) => !cancelled && setData({ id: serverId, error: errorText(e) }))
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
  return { share: current.share, error: current.error, loading: !!serverId && !current.share && !current.error, reload: () => setTick((n) => n + 1), setPublic }
}

export type PackPageState = { kind: 'loading' } | { kind: 'ready'; page: PackPage } | { kind: 'gone' } | { kind: 'busy' }

/**
 * The public page's data. The panel answers every link that doesn't open a
 * shared pack alike, so all of them are "gone".
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
        return res.status === 503 ? { kind: 'busy' } : { kind: 'gone' }
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
