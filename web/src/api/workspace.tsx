import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ApiError, get, post } from './client'
import type { MachineView, Me, ServerStatus } from './types'
import { usePoll } from '@/lib/usePoll'

export interface Workspace {
  me: Me
  servers: ServerStatus[] | undefined
  serversError: ApiError | undefined
  /** The machine this dashboard runs on (the only one for now). */
  machine: MachineView | undefined
  machines: MachineView[]
  prefs: Record<string, string>
  setPrefs: (p: Record<string, string>) => Promise<void>
  refresh: () => Promise<void>
  /** A Playkeeper update being installed; set while the dashboard restarts too. */
  updating: string | undefined
  /** When that update started, kept while the dashboard restarts. */
  updatingSince: string | undefined
  agentDown: boolean
  /**
   * The server list is the last one seen, kept only for names while the
   * agent can't be reached: nothing in it is live.
   */
  stale: boolean
  /** When the agent last answered. */
  lastSeenAt: number | undefined
  machineName: string
  /** The server the phone's tabs and More page are about. */
  lastSlug: string | undefined
  setLastSlug: (slug: string) => void
  signOut: () => Promise<void>
  /** Loads the signed-in user again, such as after a password change. */
  reloadMe: () => Promise<void>
}

const Ctx = createContext<Workspace | null>(null)

export function useWorkspace(): Workspace {
  const w = useContext(Ctx)
  if (!w) throw new Error('useWorkspace outside WorkspaceProvider')
  return w
}

/** Only for tests that render one page without the shell. */
export const WorkspaceContext = Ctx

const lastKey = 'playkeeper.lastServer'

function readLast(): string | undefined {
  try {
    return window.localStorage.getItem(lastKey) ?? undefined
  } catch {
    return undefined
  }
}

export function WorkspaceProvider({ me, onMe, onSignedOut, children }: { me: Me; onMe: (m: Me) => void; onSignedOut: () => void; children: ReactNode }) {
  const servers = usePoll(() => get<ServerStatus[]>('/api/servers'), 3000)
  const machines = usePoll(() => get<MachineView[]>('/api/machines'), 5000)
  const [prefs, setPrefsState] = useState<Record<string, string>>({})
  const [lastSlug, setLast] = useState<string | undefined>(readLast)
  useEffect(() => {
    get<Record<string, string>>('/api/me/prefs')
      .then(setPrefsState)
      .catch(() => undefined)
  }, [])
  const setPrefs = useCallback(async (p: Record<string, string>) => {
    setPrefsState(await post<Record<string, string>>('/api/me/prefs', p))
  }, [])
  const setLastSlug = useCallback((slug: string) => {
    setLast(slug)
    try {
      window.localStorage.setItem(lastKey, slug)
    } catch {
      // Private browsing may refuse storage; the phone tabs then fall back to the first server.
    }
  }, [])
  const signOut = useCallback(async () => {
    try {
      await post('/api/auth/logout')
    } finally {
      onSignedOut()
    }
  }, [onSignedOut])
  const reloadMe = useCallback(async () => {
    onMe(await get<Me>('/api/auth/me'))
  }, [onMe])

  const machine = machines.data?.[0]
  const live = machine?.live
  // While Playkeeper installs an update the agent and panel restart; the
  // failed polls in between are expected, not an outage.
  const installing = useRef<{ version: string; since?: string } | undefined>(undefined)
  if (live?.updateInstalling) installing.current = { version: live.updateInstalling, since: live.operation?.kind === 'update' ? live.operation.startedAt : installing.current?.since }
  else if (live && !machines.error) installing.current = undefined
  const updating = machines.error ? installing.current?.version : live?.updateInstalling
  const updatingSince = updating ? installing.current?.since : undefined
  const agentDown = !updating && (servers.error?.code === 'agent_unavailable' || machine?.error?.code === 'agent_unavailable')
  const known = useRef<{ list: ServerStatus[]; at: number } | undefined>(undefined)
  if (servers.data) known.current = { list: servers.data, at: Date.now() }
  const stale = !servers.data && (agentDown || !!updating) && !!known.current
  const serverList = servers.data ?? (stale ? known.current?.list : undefined)
  const knownMachine = useRef<MachineView | undefined>(undefined)
  if (machine) knownMachine.current = machine
  const shownMachine = useMemo(() => machine ?? (stale && knownMachine.current ? { ...knownMachine.current, live: undefined } : undefined), [machine, stale])

  // After an update the page still runs the previous version's code; load
  // the new one once the new version answers.
  useEffect(() => {
    if (live && !machines.error && !live.updateInstalling && live.agentVersion && me.version && live.agentVersion !== me.version) window.location.reload()
  }, [live, machines.error, me.version])

  const refresh = useCallback(async () => {
    await Promise.all([servers.refresh(), machines.refresh()])
  }, [servers, machines])

  const lastSeenAt = known.current?.at
  const value = useMemo<Workspace>(
    () => ({
      me,
      servers: serverList,
      serversError: servers.error,
      machine: shownMachine,
      machines: machines.data ?? [],
      prefs,
      setPrefs,
      refresh,
      updating,
      updatingSince,
      agentDown,
      stale,
      lastSeenAt,
      machineName: shownMachine?.name || live?.hostname || '',
      lastSlug,
      setLastSlug,
      signOut,
      reloadMe,
    }),
    [me, serverList, servers.error, shownMachine, machines.data, prefs, setPrefs, refresh, updating, updatingSince, agentDown, stale, lastSeenAt, live?.hostname, lastSlug, setLastSlug, signOut, reloadMe],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

/** The server a slug names, if it is in the list. */
export function useServer(slug: string | undefined): ServerStatus | undefined {
  const { servers } = useWorkspace()
  return servers?.find((s) => s.slug === slug)
}

/** The server the phone's tabs are about: the last one opened, or the first. */
export function usePhoneServer(): ServerStatus | undefined {
  const { servers, lastSlug } = useWorkspace()
  return servers?.find((s) => s.slug === lastSlug) ?? servers?.[0]
}

export const serverApi = (id: string, rest = '') => `/api/servers/${id}${rest}`
export const machineApi = (id: string, rest = '') => `/api/machines/${id}${rest}`

/** A readable message for any error an API call throws. */
export function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
}
