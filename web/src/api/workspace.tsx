import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ApiError, get, post } from './client'
import type { MachineView, Me, ServerStatus } from './types'
import { t } from '@/i18n'
import { isStale, joinHost, machineLabel, machineOf, machineRoute, reachOf } from '@/lib/machines'
import { mergePrefs, undoPrefs } from '@/lib/optimistic'
import { usePoll } from '@/lib/usePoll'

export interface Workspace {
  me: Me
  servers: ServerStatus[] | undefined
  serversError: ApiError | undefined
  /** The machine this dashboard runs on. */
  machine: MachineView | undefined
  /** Every machine, the dashboard's own first. */
  machines: MachineView[]
  prefs: Record<string, string>
  /** Shows the change at once; if it can't be saved it's put back and the promise rejects. */
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

export function WorkspaceProvider({ me, onSignedOut, children }: { me: Me; onSignedOut: () => void; children: ReactNode }) {
  const servers = usePoll(() => get<ServerStatus[]>('/api/servers'), 3000)
  const machines = usePoll(() => get<MachineView[]>('/api/machines'), 5000)
  const [prefs, setPrefsState] = useState<Record<string, string>>({})
  const prefsRef = useRef(prefs)
  const changedPrefs = useRef(false)
  const showPrefs = useCallback((p: Record<string, string>) => {
    prefsRef.current = p
    setPrefsState(p)
  }, [])
  const [lastSlug, setLast] = useState<string | undefined>(readLast)
  useEffect(() => {
    get<Record<string, string>>('/api/me/prefs')
      .then((p) => {
        if (!changedPrefs.current) showPrefs(p)
      })
      .catch(() => undefined)
  }, [showPrefs])
  // Shown at once; put back (and the error thrown) if the panel refuses.
  const setPrefs = useCallback(
    async (change: Record<string, string>) => {
      changedPrefs.current = true
      const before = prefsRef.current
      showPrefs(mergePrefs(before, change))
      try {
        showPrefs(await post<Record<string, string>>('/api/me/prefs', change))
      } catch (e) {
        showPrefs(mergePrefs(prefsRef.current, undoPrefs(before, change)))
        throw e
      }
    },
    [showPrefs],
  )
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

  const machine = machines.data?.find((m) => m.kind === 'local') ?? machines.data?.[0]
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
  const knownMachines = useRef<MachineView[]>([])
  if (machines.data) knownMachines.current = machines.data
  const machineList = useMemo(() => machines.data ?? (stale ? knownMachines.current.map((m) => ({ ...m, live: undefined })) : []), [machines.data, stale])
  const shownMachine = machine ?? machineList.find((m) => m.kind === 'local')

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
      machines: machineList,
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
    }),
    [me, serverList, servers.error, shownMachine, machineList, prefs, setPrefs, refresh, updating, updatingSince, agentDown, stale, lastSeenAt, live?.hostname, lastSlug, setLastSlug, signOut],
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

/**
 * The machine a server runs on and whether the dashboard sees the server
 * live: stale when the machine is away, its agent doesn't answer, or the
 * status is the last one heard. offline says why controls are off then.
 * shared is whether there's more than one machine, so pages name the
 * server's.
 */
export function useServerMachine(s: ServerStatus) {
  const ws = useWorkspace()
  const machine = machineOf(s, ws.machines)
  const reach = reachOf(s, ws)
  const stale = isStale(s, ws.stale) || reach.state !== 'live'
  return {
    machine,
    reach,
    stale,
    offline: reach.state === 'away' ? t('machines.away.pill', { name: machineLabel(reach.machine) }) : stale ? t('reason.noAgent') : undefined,
    shared: ws.machines.length > 1,
    name: machineLabel(machine) || ws.machineName,
    route: machine ? machineRoute(machine) : undefined,
    host: joinHost(machine, window.location.hostname),
  }
}

export const serverApi = (id: string, rest = '') => `/api/servers/${id}${rest}`
export const machineApi = (id: string, rest = '') => `/api/machines/${id}${rest}`

/** A readable message for any error an API call throws. */
export function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
}
