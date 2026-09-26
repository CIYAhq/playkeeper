import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { PackageIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { AddonChecks, AddonDetails, AddonKey, AddonNotice, AddonPlan, Addons, Operation, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { footerFor, isAddonOp, keyFrom, keyOf, mergeRows, sameKey, voiceChatProject, type AddonKind, type AddonRow } from '@/lib/addons'
import { navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** An install or update the dialog follows. */
export interface Job {
  /** Absent while a new plan waits for a yes. */
  op?: Operation
  /** What the job does, shown until the agent reports its files. */
  plan?: AddonPlan
  title: string
  retry: () => void
  /** After the plan changed: fetch the new one to confirm. */
  lookAgain?: () => void
  /** Carries out the plan once it's confirmed. */
  confirm?: () => Promise<void>
}

/** The add-on the detail sheet shows; adoptFile is set for a file added by hand that Modrinth knows. */
export interface Detail {
  key: AddonKey
  adoptFile?: string
  details?: AddonDetails
}

/** Voice chat, which asks before it opens its port. */
export interface VoiceAsk {
  key: AddonKey
  name: string
  fingerprint?: string
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
  /** Installs the add-on; voice chat asks first, and installs with openPorts once the owner agrees. */
  install: (key: AddonKey, name: string, fingerprint?: string, openPorts?: boolean) => Promise<boolean>
  voice: VoiceAsk | undefined
  closeVoice: () => void
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

/** Why a plan can't go ahead: its first blocker, or what it needs done by hand. */
function notReady(plan: AddonPlan) {
  const n: AddonNotice | undefined = plan.blockers[0] ?? plan.manual[0]
  toastManager.add({ title: n?.message ?? t('addons.failedTitle'), description: n?.hint, type: 'error' })
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
  const [voice, setVoice] = useState<VoiceAsk>()
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
  const jobId = job?.op?.id
  const jobRunning = job?.op?.status === 'running'
  useEffect(() => {
    if (!jobId || !jobRunning || !machineId) return
    let stopped = false
    const tick = async () => {
      try {
        const op = await get<Operation>(machineApi(machineId, `/operations/${jobId}`))
        if (!stopped) setJob((j) => (j && j.op?.id === op.id ? { ...j, op } : j))
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
    async (key: AddonKey, name: string, fingerprint?: string, openPorts = false): Promise<boolean> => {
      if (!openPorts && sameKey(key, voiceChatProject)) {
        setDetail(undefined)
        setVoice({ key, name, fingerprint })
        return true
      }
      try {
        const op = await post<Operation>(serverApi(id, '/addons/install'), { source: key.source, projectId: key.projectId, fingerprint, openPorts: openPorts || undefined })
        setDetail(undefined)
        setVoice(undefined)
        // The plan may have changed since: confirm the new one, or show why not.
        const again = () => {
          void get<AddonDetails>(detailsPath(id, key))
            .then((d) => {
              const f = footerFor(d)
              if (f.kind === 'install' && d.plan && d.plan.steps.length <= 1) return void install(key, name, f.fingerprint, openPorts)
              setJob(undefined)
              setDetail({ key, details: d })
            })
            .catch(toastError)
        }
        setJob({ op, title: t('addons.installing', { name }), retry: again, lookAgain: again })
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
      const body = { addons: keys?.map(keyFrom), changed: changed || undefined }
      const retry = () => void update(keys, title, changed)
      // The update carries the fingerprint of the plan it shows; the agent
      // refuses it when the plan has changed since.
      async function send(plan: AddonPlan) {
        const op = await post<Operation>(serverApi(id, '/addons/update'), { ...body, fingerprint: plan.fingerprint })
        setJob({ op, plan, title, retry, lookAgain })
      }
      function lookAgain() {
        post<AddonPlan>(serverApi(id, '/addons/update/plan'), body)
          .then((plan) => {
            if (!plan.ready) {
              setJob(undefined)
              return notReady(plan)
            }
            setJob({ plan, title, retry, confirm: () => send(plan).catch(toastError) })
          })
          .catch(toastError)
      }
      try {
        const plan = await post<AddonPlan>(serverApi(id, '/addons/update/plan'), body)
        if (!plan.ready) {
          notReady(plan)
          return false
        }
        await send(plan)
        setDetail(undefined)
        setAsking(undefined)
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
    voice,
    closeVoice: () => setVoice(undefined),
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
      className={cn('flex shrink-0 items-center justify-center overflow-hidden rounded-[10px] border border-border bg-muted text-muted-foreground transition-opacity duration-(--motion-standard) ease-standard', dim && 'opacity-50', className)}
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
