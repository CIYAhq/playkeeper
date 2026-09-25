import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { PackageIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { AddonChecks, AddonDetails, AddonKey, Addons, Operation, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { footerFor, isAddonOp, keyFrom, keyOf, mergeRows, sameKey, type AddonKind, type AddonRow } from '@/lib/addons'
import { navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** Motion for the tab in one place, so the shared motion tokens can replace it. */
export const motion = {
  enter: 'animate-in fade-in-0 slide-in-from-bottom-1 duration-200 ease-out',
  fade: 'animate-in fade-in-0 duration-200 ease-out',
  press: 'transition-[background-color,box-shadow,opacity] duration-150 active:scale-[0.99]',
}

/** An install or update the dialog follows. */
export interface Job {
  op: Operation
  title: string
  retry: () => void
}

/** The add-on the detail sheet shows; adoptFile is set for a file added by hand that Modrinth knows. */
export interface Detail {
  key: AddonKey
  adoptFile?: string
  details?: AddonDetails
}

/** An update of a file that changed since it was installed, which asks first. */
export interface Ask {
  key: AddonKey
  name: string
  version: string
}

interface AddonsState {
  server: ServerStatus
  kind: AddonKind
  tab: 'plugins' | 'mods'
  addons: Addons | undefined
  rows: AddonRow[]
  error: ApiError | undefined
  loading: boolean
  refresh: () => Promise<void>
  detail: Detail | undefined
  openDetail: (d: Detail | undefined) => void
  job: Job | undefined
  closeJob: () => void
  removing: AddonKey | undefined
  askRemove: (k: AddonKey | undefined) => void
  asking: Ask | undefined
  askUpdate: (a: Ask | undefined) => void
  highlight: string | undefined
  install: (key: AddonKey, name: string, fingerprint?: string) => Promise<boolean>
  update: (keys: AddonKey[] | undefined, title: string, changed?: boolean) => Promise<boolean>
  adopt: (fileName: string) => Promise<boolean>
  forget: (key: AddonKey) => Promise<boolean>
  restart: () => Promise<boolean>
  openSource: (key: AddonKey) => void
  goToFile: (file: string, name: string) => void
}

const Ctx = createContext<AddonsState | null>(null)

export function useAddons(): AddonsState {
  const v = useContext(Ctx)
  if (!v) throw new Error('useAddons outside AddonsProvider')
  return v
}

function toastError(e: unknown) {
  toastManager.add({ title: errorText(e), type: 'error' })
}

export const detailsPath = (serverId: string, k: AddonKey) => serverApi(serverId, `/addons/project/${k.source}/${encodeURIComponent(k.projectId)}`)

export function AddonsProvider({ server, kind, children }: { server: ServerStatus; kind: AddonKind; children: ReactNode }) {
  const ws = useWorkspace()
  const id = server.id
  const list = usePoll(() => get<Addons>(serverApi(id, '/addons')), 10_000, id)
  const addons = list.data
  // The checks ask Modrinth and Hangar; the agent caches them by the folder's
  // state, so they are asked again only when the folder changes.
  const folder = addons ? `${addons.files.map((f) => `${f.fileName}:${f.status}`).join('|')}#${addons.missing.map(keyOf).join('|')}` : ''
  const checks = usePoll(() => (folder ? get<AddonChecks>(serverApi(id, '/addons/checks')) : Promise.resolve(undefined)), 15 * 60_000, `${id}/${folder}`)
  const rows = useMemo(() => (addons ? mergeRows(addons, checks.data) : []), [addons, checks.data])

  const [detail, setDetail] = useState<Detail>()
  const [job, setJob] = useState<Job>()
  const [removing, setRemoving] = useState<AddonKey>()
  const [asking, setAsking] = useState<Ask>()
  const [highlight, setHighlight] = useState<string>()

  const refreshList = list.refresh
  const refreshChecks = checks.refresh
  const refresh = useCallback(async () => {
    await refreshList()
    await refreshChecks()
  }, [refreshList, refreshChecks])

  // Follow the job's operation once a second: its files are published at
  // most that often.
  const machineId = server.machineId ?? ws.machine?.id
  const jobId = job?.op.id
  const jobRunning = job?.op.status === 'running'
  useEffect(() => {
    if (!jobId || !jobRunning || !machineId) return
    let stopped = false
    const tick = async () => {
      try {
        const op = await get<Operation>(machineApi(machineId, `/operations/${jobId}`))
        if (!stopped) setJob((j) => (j && j.op.id === op.id ? { ...j, op } : j))
      } catch {
        // The next tick tries again; the dialog keeps the last state.
      }
    }
    const timer = window.setInterval(() => void tick(), 1000)
    return () => {
      stopped = true
      window.clearInterval(timer)
    }
  }, [jobId, jobRunning, machineId])

  const wasRunning = useRef(jobRunning)
  useEffect(() => {
    if (wasRunning.current && !jobRunning) void refresh()
    wasRunning.current = jobRunning
  }, [jobRunning, refresh])

  // A job whose dialog was closed still finishes: refresh when the server's
  // add-on operation ends.
  const serverOp = isAddonOp(server.operation) ? server.operation.id : undefined
  const lastOp = useRef(serverOp)
  useEffect(() => {
    if (lastOp.current && !serverOp) void refresh()
    lastOp.current = serverOp
  }, [serverOp, refresh])

  const install = useCallback(
    async (key: AddonKey, name: string, fingerprint?: string): Promise<boolean> => {
      try {
        const op = await post<Operation>(serverApi(id, '/addons/install'), { source: key.source, projectId: key.projectId, fingerprint })
        setDetail(undefined)
        setJob({
          op,
          title: t('addons.installing', { name }),
          retry: () => {
            // The plan may have changed since: confirm the new one, or show why not.
            void get<AddonDetails>(detailsPath(id, key))
              .then((d) => {
                const f = footerFor(d)
                if (f.kind === 'install' && d.plan && d.plan.steps.length <= 1) return void install(key, name, f.fingerprint)
                setJob(undefined)
                setDetail({ key, details: d })
              })
              .catch(toastError)
          },
        })
        return true
      } catch (e) {
        toastError(e)
        return false
      }
    },
    [id],
  )

  const update = useCallback(
    async (keys: AddonKey[] | undefined, title: string, changed = false): Promise<boolean> => {
      try {
        const op = await post<Operation>(serverApi(id, '/addons/update'), { addons: keys?.map(keyFrom), changed: changed || undefined })
        setDetail(undefined)
        setAsking(undefined)
        setJob({ op, title, retry: () => void update(keys, title, changed) })
        return true
      } catch (e) {
        toastError(e)
        return false
      }
    },
    [id],
  )

  const adopt = useCallback(
    async (fileName: string): Promise<boolean> => {
      try {
        await post(serverApi(id, '/addons/adopt'), { fileName })
        setDetail(undefined)
        await refresh()
        return true
      } catch (e) {
        toastError(e)
        return false
      }
    },
    [id, refresh],
  )

  const forget = useCallback(
    async (key: AddonKey): Promise<boolean> => {
      try {
        await post(serverApi(id, '/addons/forget'), { source: key.source, projectId: key.projectId })
        setDetail(undefined)
        await refresh()
        return true
      } catch (e) {
        toastError(e)
        return false
      }
    },
    [id, refresh],
  )

  const restart = useCallback(async (): Promise<boolean> => {
    try {
      await post(serverApi(id, '/restart'))
      setJob(undefined)
      window.setTimeout(() => void refreshList(), 5000)
      return true
    } catch (e) {
      toastError(e)
      return false
    }
  }, [id, refreshList])

  // Installed records don't carry their page (Hangar's needs the owner), so
  // the link comes from the add-on's details. The tab opens first, while the
  // click still counts as the user's.
  const openSource = useCallback(
    (key: AddonKey) => {
      const w = window.open('', '_blank')
      if (w) w.opener = null
      get<AddonDetails>(detailsPath(id, key))
        .then((d) => {
          if (w && d.card.pageUrl) w.location.href = d.card.pageUrl
          else w?.close()
        })
        .catch((e) => {
          w?.close()
          toastError(e)
        })
    },
    [id],
  )

  const tab = kind === 'plugin' ? 'plugins' : 'mods'
  const goToFile = useCallback(
    (file: string, name: string) => {
      const row = rows.find((r) => r.fileName === file) ?? rows.find((r) => r.name === name)
      if (row?.addon && row.state !== 'unknown') {
        setDetail(row.state === 'identified' ? { key: row.addon, adoptFile: row.fileName } : { key: row.addon })
        return
      }
      setDetail(undefined)
      navigate({ name: 'server', slug: server.slug, tab })
      if (row) {
        setHighlight(row.id)
        window.setTimeout(() => setHighlight((h) => (h === row.id ? undefined : h)), 2400)
      }
    },
    [rows, server.slug, tab],
  )

  useEffect(() => {
    if (!highlight) return
    document.getElementById(rowDomId(highlight))?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [highlight])

  const value: AddonsState = {
    server,
    kind,
    tab,
    addons,
    rows,
    error: list.error,
    loading: list.loading && !addons,
    refresh,
    detail,
    openDetail: setDetail,
    job,
    closeJob: () => setJob(undefined),
    removing,
    // The sheet makes way for the dialogs it opens.
    askRemove: (k) => {
      if (k) setDetail(undefined)
      setRemoving(k)
    },
    asking,
    askUpdate: (x) => {
      if (x) setDetail(undefined)
      setAsking(x)
    },
    highlight,
    install,
    update,
    adopt,
    forget,
    restart,
    openSource,
    goToFile,
  }
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export const rowDomId = (id: string) => `addon-${id.replace(/[^\w-]/g, '_')}`

/** Whether the row stands for the add-on with this key. */
export function rowIs(r: AddonRow, k: AddonKey): boolean {
  return !!r.addon && sameKey(r.addon, k)
}

/** An add-on's icon through the panel, or a package when it has none. */
export function AddonIcon({ url, size = 40, dim, className }: { url?: string; size?: number; dim?: boolean; className?: string }) {
  const { server } = useAddons()
  const [failed, setFailed] = useState<string>()
  const show = url && failed !== url
  return (
    <span
      className={cn('flex shrink-0 items-center justify-center overflow-hidden rounded-[10px] border border-border bg-muted text-muted-foreground transition-opacity', dim && 'opacity-50', className)}
      style={{ width: size, height: size }}
      aria-hidden="true"
    >
      {show ? (
        <img src={serverApi(server.id, `/addons/icon?url=${encodeURIComponent(url)}`)} alt="" width={size} height={size} loading="lazy" decoding="async" className="size-full object-cover" onError={() => setFailed(url)} />
      ) : (
        <PackageIcon className="size-[45%]" strokeWidth={1.75} />
      )}
    </span>
  )
}
