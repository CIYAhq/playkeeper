import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronRightIcon, CircleAlertIcon, RefreshCwIcon, Trash2Icon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { DiskCandidate, DiskGroup, DiskReport, DiskServer, DiskWay, OffsiteView, Operation } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { Emblem } from '@/components/app/art'
import { Card, Notice, SectionLabel, Spinner } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { formatLocale, t, type MessageKey } from '@/i18n'
import { formatBytes, formatClock, formatDate, formatList } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { iconURL } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { viewerTimeZone } from '@/lib/when'

// Wave 7: the machine's Disk space page. Nothing is deleted from it without a
// review (or, for what can't be reviewed item by item, a confirm) first.

const GiB = 2 ** 30
const TiB = 2 ** 40

function oneDecimal(v: number): string {
  return new Intl.NumberFormat(formatLocale(), { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(v)
}

/** A size to compare with others: "480 MB", "1.0 GB", "18.2 GB". */
function sizeText(n: number): string {
  if (n >= TiB) return t('unit.tb', { value: oneDecimal(n / TiB) })
  if (n >= GiB) return t('unit.gb', { value: oneDecimal(n / GiB) })
  return formatBytes(n)
}

/** A table cell: gigabytes down to 0.1 GB, so a column reads at a glance. */
function cellText(n: number): string {
  return n >= GiB / 10 && n < TiB ? t('unit.gb', { value: oneDecimal(n / GiB) }) : sizeText(n)
}

const groups: Record<DiskGroup, { key: MessageKey; color: string }> = {
  backups: { key: 'disk.group.backups', color: 'bg-primary' },
  worlds: { key: 'disk.group.worlds', color: 'bg-disk-worlds' },
  server_files: { key: 'disk.group.serverFiles', color: 'bg-ring' },
  logs: { key: 'disk.group.logs', color: 'bg-marigold' },
  other: { key: 'disk.group.other', color: 'bg-disk-other' },
  free: { key: 'disk.group.free', color: 'bg-accent' },
}

function useDiskScan(machineId: string | undefined) {
  const [report, setReport] = useState<DiskReport>()
  const [error, setError] = useState<ApiError>()
  const [scanning, setScanning] = useState(false)
  const latest = useRef(0)
  const scan = useCallback(
    async (fresh: boolean): Promise<ApiError | undefined> => {
      if (!machineId) return undefined
      const n = ++latest.current
      setScanning(true)
      try {
        const q = new URLSearchParams({ tz: viewerTimeZone() })
        if (fresh) q.set('fresh', '1')
        const r = await get<DiskReport>(machineApi(machineId, `/disk?${q.toString()}`))
        if (n === latest.current) {
          setReport(r)
          setError(undefined)
        }
        return undefined
      } catch (e) {
        const err = e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' })
        if (n === latest.current) setError(err)
        return err
      } finally {
        if (n === latest.current) setScanning(false)
      }
    },
    [machineId],
  )
  useEffect(() => {
    void scan(false)
  }, [scan])
  return { report, error, scanning, scan }
}

/** Where the servers' backups also have copies ("Backblaze B2"), if anywhere. */
function useCopiesPlace(serverIds: string[]): string | undefined {
  const [place, setPlace] = useState<string>()
  const key = serverIds.join(',')
  useEffect(() => {
    setPlace(undefined)
    if (!key) return
    let live = true
    void Promise.allSettled(key.split(',').map((id) => get<OffsiteView>(serverApi(id, '/offsite')))).then((all) => {
      if (!live) return
      const places = [...new Set(all.flatMap((r) => (r.status === 'fulfilled' && r.value.enabled && r.value.copies > 0 && r.value.place ? [r.value.place] : [])))]
      setPlace(places.length ? formatList(places) : undefined)
    })
    return () => {
      live = false
    }
  }, [key])
  return place
}

const pollEvery = 800

/** Runs the agent's clean-up and waits for it to finish. */
async function cleanUp(machineId: string, body: { ids: string[] } | { ways: string[] }, alive: () => boolean): Promise<Operation> {
  let op = await post<Operation>(machineApi(machineId, '/disk/clean'), { ...body, timeZone: viewerTimeZone() })
  while (op.status === 'running' && alive()) {
    await new Promise((resolve) => window.setTimeout(resolve, pollEvery))
    op = await get<Operation>(machineApi(machineId, `/operations/${op.id}`))
  }
  return op
}

function cleanedToast(op: Operation) {
  if (op.status === 'running') return
  if (op.status === 'failed') {
    toastManager.add({ title: op.error || t('disk.failed'), description: op.hint, type: 'error' })
    return
  }
  const freed = sizeText(Number(op.detail?.freed ?? 0))
  const raw = op.detail?.problems
  const problems = Array.isArray(raw) ? (raw as { text?: string }[]) : []
  const count = problems.length + Number(op.detail?.moreProblems ?? 0)
  if (count > 0) toastManager.add({ title: t('disk.freedSome', { size: freed, count }), description: problems[0]?.text, type: 'warning' })
  else toastManager.add({ title: t('disk.freed', { size: freed }), type: 'success' })
}

function fromText(way: DiskWay, names: Map<string, string>): string {
  if (way.everyServer) return t('disk.fromEvery')
  const list = (way.serverIds ?? []).map((id) => names.get(id) ?? id)
  if (!list.length) return ''
  const shown = list.length > 3 ? [...list.slice(0, 3), t('disk.moreServers', { count: list.length - 3 })] : list
  return t('disk.from', { servers: formatList(shown) })
}

function versionsText(way: DiskWay): string {
  const vs = way.versions ?? []
  const first = vs[0]
  if (!first) return t('disk.replacedAny')
  const label = (v: { version: string; build?: string }) => (v.build ? t('disk.build', { version: v.version, build: v.build }) : v.version)
  const cap = (items: string[]) => (items.length > 3 ? [...items.slice(0, 3), t('disk.moreServers', { count: items.length - 3 })] : items)
  const versions = vs.every((v) => v.software === first.software)
    ? t('disk.software', { software: first.software, versions: formatList(cap(vs.map(label))) }).trim()
    : formatList(cap(vs.map((v) => t('disk.software', { software: v.software, versions: label(v) }).trim())))
  return t('disk.replaced', { versions })
}

function ageTitle(way: DiskWay, days: MessageKey, hours: MessageKey): string {
  const h = Number(way.params?.hours ?? 0)
  return h > 0 ? t(hours, { count: h }) : t(days, { count: Number(way.params?.days ?? 30) })
}

function wayTitle(way: DiskWay): string {
  switch (way.id) {
    case 'old_backups':
      return t('disk.way.oldBackups')
    case 'old_logs':
      return ageTitle(way, 'disk.way.oldLogs', 'disk.way.oldLogsHours')
    case 'old_crash_reports':
      return ageTitle(way, 'disk.way.oldCrashReports', 'disk.way.oldCrashReportsHours')
    case 'unused_software':
      return t('disk.way.unusedSoftware')
    case 'downloads':
      return t('disk.way.downloads')
    case 'set_aside':
      return t('disk.way.setAside')
    case 'unfinished':
      return t('disk.way.unfinished')
    default: {
      const unknown: never = way.id
      return way.title || String(unknown)
    }
  }
}

function wayText(way: DiskWay, names: Map<string, string>): string {
  switch (way.id) {
    case 'old_backups':
      return t('disk.way.oldBackupsText', { count: way.candidateIds.length })
    case 'old_logs':
    case 'old_crash_reports':
    case 'set_aside':
      return fromText(way, names)
    case 'unused_software':
      return versionsText(way)
    case 'downloads':
      return t('disk.way.downloadsText')
    case 'unfinished':
      return t('disk.way.unfinishedText')
    default: {
      const unknown: never = way.id
      return way.text || String(unknown)
    }
  }
}

interface ReviewRow {
  key: string
  ids: string[]
  label: string
  hint?: string
  bytes: number
}

function whenOf(c: DiskCandidate): string {
  if (c.params?.createdAt) return c.params.createdAt
  // A set-aside folder is dated by its name, as a day in the scan's time
  // zone (the viewer's); its files keep the times they had when set aside.
  if (c.reason === 'leftover_copy' && c.params?.date) return `${c.params.date}T12:00:00`
  return c.modifiedAt
}

/** "Sat 19 Sep" in the viewer's language. */
function shortDay(iso: string): string {
  return new Date(iso).toLocaleDateString(formatLocale(), { weekday: 'short', day: 'numeric', month: 'short' })
}

function itemRow(c: DiskCandidate, server: string): ReviewRow {
  const row = { key: c.id, ids: [c.id], bytes: c.bytes }
  if (c.reason === 'pruned_backup') {
    const at = whenOf(c)
    return { ...row, label: t('disk.item', { server, what: t('disk.at', { date: shortDay(at), time: formatClock(at) }) }) }
  }
  if (c.reason === 'leftover_copy') {
    const date = formatDate(whenOf(c))
    switch (c.params?.why) {
      case 'failed_update':
        return { ...row, label: t('disk.item', { server, what: t('disk.setAside.failedUpdate', { date }) }), hint: t('disk.setAside.failedUpdateHint') }
      case 'failed_restore':
        return { ...row, label: t('disk.item', { server, what: t('disk.setAside.failedRestore', { date }) }), hint: t('disk.setAside.failedRestoreHint') }
      default:
        return { ...row, label: t('disk.item', { server, what: t('disk.setAside.replaced', { date }) }), hint: t('disk.setAside.replacedHint') }
    }
  }
  return { ...row, label: t('disk.item', { server, what: c.text }) }
}

/**
 * The rows of a review: each candidate by server and newest first, and past
 * the first `limit` one row per server for the rest ("3 more of Creative").
 */
function reviewRows(way: DiskWay, report: DiskReport, limit: number, withRange: boolean, machine: string): ReviewRow[] {
  const byId = new Map(report.candidates.map((c) => [c.id, c]))
  const names = new Map(report.servers.map((s) => [s.id, s.name]))
  const rank = new Map(report.servers.map((s, i) => [s.id, i]))
  const place = (c: DiskCandidate) => rank.get(c.serverId ?? '') ?? rank.size
  const cs = way.candidateIds.flatMap((id) => byId.get(id) ?? [])
  cs.sort((a, b) => place(a) - place(b) || Date.parse(whenOf(b)) - Date.parse(whenOf(a)))
  const shown = cs.length > limit + 1 ? cs.slice(0, limit) : cs
  const rows = shown.map((c) => itemRow(c, names.get(c.serverId ?? '') ?? machine))
  const rest = new Map<string, DiskCandidate[]>()
  for (const c of cs.slice(shown.length)) rest.set(c.serverId ?? '', [...(rest.get(c.serverId ?? '') ?? []), c])
  for (const [id, group] of rest) {
    const times = group.map(whenOf).sort((a, b) => Date.parse(a) - Date.parse(b))
    const from = shortDay(times[0] ?? '')
    const to = shortDay(times[times.length - 1] ?? '')
    rows.push({
      key: `more-${id}`,
      ids: group.map((c) => c.id),
      label: t('disk.more', { count: group.length, server: names.get(id) ?? machine }),
      hint: withRange ? (from === to ? from : t('disk.range', { from, to })) : undefined,
      bytes: group.reduce((n, c) => n + c.bytes, 0),
    })
  }
  return rows
}

export function DiskPage({ id }: { id: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const m = ws.machines.find((x) => x.id === id) ?? (ws.machine?.id === id ? ws.machine : undefined)
  const name = m ? m.name || m.live?.hostname || '' : ''
  const { report, error, scanning, scan } = useDiskScan(m?.id)
  const [open, setOpen] = useState<DiskWay>()
  const [busy, setBusy] = useState<string>()
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  const oldBackups = report?.ways.find((w) => w.id === 'old_backups')
  const copiesPlace = useCopiesPlace(oldBackups?.serverIds ?? [])

  if (!m) {
    return (
      <PageBody>
        <p className="text-sm text-muted-foreground">{ws.machines.length ? t('machine.notFound') : t('common.loading')}</p>
      </PageBody>
    )
  }

  const names = new Map((report?.servers ?? []).map((s) => [s.id, s.name]))
  const disk = report?.disk ?? undefined

  const again = async () => {
    const err = await scan(true)
    if (err) toastManager.add({ title: errorText(err), description: err.hint, type: 'error' })
  }

  const remove = async (way: DiskWay, ids?: string[]) => {
    setOpen(undefined)
    setBusy(way.id)
    try {
      cleanedToast(await cleanUp(m.id, ids ? { ids } : { ways: [way.id] }, () => alive.current))
    } catch (e) {
      toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })
    }
    if (!alive.current) return
    await scan(true)
    setBusy(undefined)
  }

  let body
  if (!report && error) {
    body = <Notice tone="error" title={errorText(error)} action={<Button variant="outline" size="sm" onClick={() => void scan(true)} loading={scanning}>{t('common.tryAgain')}</Button>} className={cn(phone && 'mt-2 px-1')} />
  } else if (!report) {
    body = <DiskSkeleton phone={phone} />
  } else {
    body = (
      <div className={cn('flex flex-col', phone ? 'gap-5 pb-6' : 'gap-6')}>
        <UsageCard report={report} machine={name} phone={phone} scanning={scanning} onScan={() => void again()} />
        <Ways report={report} names={names} phone={phone} busy={busy} onOpen={setOpen} />
        {!phone && report.servers.length > 0 && <ServerTable servers={report.servers} />}
      </div>
    )
  }

  return (
    <>
      {phone ? (
        <PhoneBackHeader to={{ name: 'machine', id: m.id }} label={name} title={t('disk.title')} />
      ) : (
        <PageHeader
          breadcrumb={
            <span className="flex items-center gap-1.5">
              <a {...linkProps({ name: 'machine', id: m.id })} className="rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
                {name}
              </a>
              <span className="text-muted-foreground/60" aria-hidden="true">
                /
              </span>
              <span className="font-semibold text-foreground">{t('disk.title')}</span>
            </span>
          }
          title={t('disk.title')}
          subtitle={disk ? t('disk.lead', { machine: name, free: formatBytes(disk.free), total: formatBytes(disk.total) }) : name}
        />
      )}
      <PageBody className={cn(phone && 'pt-2')}>{body}</PageBody>
      {open && report && open.action === 'review' && (
        <ReviewDialog way={open} report={report} machine={name} phone={phone} copiesPlace={open.id === 'old_backups' ? copiesPlace : undefined} onClose={() => setOpen(undefined)} onDelete={(ids) => void remove(open, ids)} />
      )}
      {open && report && open.action !== 'review' && <ConfirmDialog way={open} names={names} phone={phone} onClose={() => setOpen(undefined)} onDelete={() => void remove(open)} />}
    </>
  )
}

function DiskBar({ bar }: { bar: { group: DiskGroup; bytes: number }[] }) {
  const parts = bar.filter((b) => b.bytes > 0 && groups[b.group])
  const sum = parts.reduce((n, b) => n + b.bytes, 0) || 1
  return (
    <div role="img" aria-label={t('disk.barLabel')} className="mt-3 flex h-3 w-full gap-0.5 overflow-hidden rounded-full bg-accent">
      {parts.map((b) => (
        <span key={b.group} className={cn('h-full min-w-1 transition-[flex-grow] duration-500 ease-out', groups[b.group].color)} style={{ flexGrow: (b.bytes / sum) * 1000, flexBasis: 0 }} />
      ))}
    </div>
  )
}

function Legend({ bar, machine, phone }: { bar: { group: DiskGroup; bytes: number }[]; machine: string; phone: boolean }) {
  return (
    <ul className={cn('mt-3 flex flex-wrap gap-x-4 text-[13px]', phone ? 'gap-y-3' : 'gap-y-1.5')}>
      {bar
        .filter((b) => groups[b.group])
        .map((b) => (
          <li key={b.group} className="flex items-center gap-1.5">
            <span className={cn('size-2 shrink-0 rounded-full', groups[b.group].color, b.group === 'free' && 'ring-1 ring-border ring-inset')} aria-hidden="true" />
            <span>{t(groups[b.group].key, { machine })}</span>
            <span className="text-muted-foreground tabular-nums">{b.group === 'free' ? formatBytes(b.bytes) : sizeText(b.bytes)}</span>
          </li>
        ))}
    </ul>
  )
}

function cappedText(report: DiskReport): string {
  const codes = new Set((report.problems ?? []).map((p) => p.code))
  if (codes.has('too_many_files')) return t('disk.cappedFiles')
  if (codes.has('too_deep')) return t('disk.cappedDeep')
  return t('disk.cappedOther')
}

function UsageCard({ report, machine, phone, scanning, onScan }: { report: DiskReport; machine: string; phone: boolean; scanning: boolean; onScan: () => void }) {
  const disk = report.disk
  const again = (label: string) =>
    phone ? (
      <Button variant="outline" size="touch" className="mt-4 w-full" onClick={onScan} loading={scanning}>
        <RefreshCwIcon />
        {label}
      </Button>
    ) : (
      <div className="mt-4 border-t border-border pt-3">
        <Button variant="outline" size="sm" onClick={onScan} loading={scanning}>
          <RefreshCwIcon />
          {label}
        </Button>
      </div>
    )
  if (!disk) {
    const reason = report.problems?.find((p) => p.code === 'disk_space')?.text
    return (
      <Card className={cn(phone && 'p-4')} role="alert">
        <p className={cn('flex items-center gap-2 font-semibold', phone ? 'text-[17px]' : 'text-base')}>
          <CircleAlertIcon className="size-5 shrink-0 text-destructive-foreground" aria-hidden="true" />
          {t('disk.unreadable', { machine })}
        </p>
        {reason && <p className={cn('mt-2 text-muted-foreground', phone ? 'text-sm' : 'text-[13px]')}>{reason}</p>}
        {!phone && <p className="mt-2 text-[13px] text-muted-foreground">{t('disk.fromFolders')}</p>}
        {again(t('disk.checkAgain'))}
      </Card>
    )
  }
  const capped = report.truncated
  return (
    <Card className={cn(phone && 'p-4')}>
      <p className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-[22px] leading-7 font-bold tracking-[-0.015em]">{t(capped ? 'disk.usedAtLeast' : 'disk.used', { size: formatBytes(disk.used) })}</span>
        <span className="text-[13px] text-muted-foreground">{phone || capped ? t('disk.of', { total: formatBytes(disk.total) }) : t('disk.ofFree', { total: formatBytes(disk.total), free: formatBytes(disk.free) })}</span>
      </p>
      <DiskBar bar={disk.bar} />
      {capped ? (
        <>
          <p className="mt-3 text-[13px] text-muted-foreground">{cappedText(report)}</p>
          {again(t('disk.scanAgain'))}
        </>
      ) : (
        <Legend bar={disk.bar} machine={machine} phone={phone} />
      )}
    </Card>
  )
}

function Ways({ report, names, phone, busy, onOpen }: { report: DiskReport; names: Map<string, string>; phone: boolean; busy: string | undefined; onOpen: (way: DiskWay) => void }) {
  const ways = report.ways
  if (phone) {
    return (
      <section aria-labelledby="disk-ways">
        <SectionLabel className="px-4">
          <span id="disk-ways">{ways.length ? t('disk.waysPhone', { size: sizeText(report.freeable) }) : t('disk.ways')}</span>
        </SectionLabel>
        {ways.length ? (
          <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {ways.map((w) => (
              <li key={w.id} className="border-b border-border last:border-b-0">
                <button type="button" disabled={!!busy} onClick={() => onOpen(w)} className="flex min-h-14 w-full items-center gap-3 py-3 pr-3 pl-4 text-left outline-none active:bg-accent/60 focus-visible:bg-accent/60 disabled:opacity-60">
                  <span className="min-w-0 flex-1 text-base leading-[22px]">{wayTitle(w)}</span>
                  <span className="text-[15px] text-muted-foreground tabular-nums">{sizeText(w.bytes)}</span>
                  {busy === w.id ? <Spinner className="size-5 text-info" /> : <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-2 px-4 text-[15px] text-muted-foreground">{t('disk.waysNone')}</p>
        )}
      </section>
    )
  }
  return (
    <section aria-labelledby="disk-ways">
      <h2 id="disk-ways" className="text-[15px] leading-5 font-semibold">
        {t('disk.ways')}
      </h2>
      <p className="mt-0.5 text-xs text-muted-foreground">{ways.length ? t('disk.waysTotal', { size: sizeText(report.freeable) }) : t('disk.waysNone')}</p>
      {ways.length > 0 && (
        <ul className="mt-3 overflow-hidden rounded-2xl border border-border bg-white">
          {ways.map((w) => (
            <li key={w.id} className="flex min-h-[62px] items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
              <span className="min-w-0 flex-1">
                <span className="block text-sm leading-5 font-semibold">{wayTitle(w)}</span>
                <span className="block truncate text-xs text-muted-foreground">{wayText(w, names)}</span>
              </span>
              <span className="text-[13px] font-semibold tabular-nums">{sizeText(w.bytes)}</span>
              <Button variant="outline" size="sm" onClick={() => onOpen(w)} loading={busy === w.id} disabled={!!busy && busy !== w.id}>
                {w.action === 'review' ? t('disk.review') : w.action === 'clear' ? t('common.clear') : t('disk.delete')}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ServerTable({ servers }: { servers: DiskServer[] }) {
  const ws = useWorkspace()
  const cell = 'w-[118px] px-4 text-right tabular-nums'
  const bytes = (s: DiskServer, g: DiskGroup) => s.groups.find((x) => x.group === g)?.bytes ?? 0
  return (
    <section aria-labelledby="disk-servers">
      <h2 id="disk-servers" className="text-[15px] leading-5 font-semibold">
        {t('disk.byServer')}
      </h2>
      <div className="mt-3 overflow-hidden rounded-2xl border border-border">
        <table className="w-full border-collapse text-[13px]">
          <thead className="bg-muted text-xs text-muted-foreground">
            <tr className="h-9">
              <th scope="col" className="px-4 text-left font-normal">
                {t('disk.col.server')}
              </th>
              <th scope="col" className={cn(cell, 'font-normal')}>
                {t('disk.col.world')}
              </th>
              <th scope="col" className={cn(cell, 'font-normal')}>
                {t('disk.col.serverFiles')}
              </th>
              <th scope="col" className={cn(cell, 'font-normal')}>
                {t('disk.col.backups')}
              </th>
              <th scope="col" className={cn(cell, 'font-normal')}>
                {t('disk.col.logs')}
              </th>
              <th scope="col" className={cn(cell, 'font-normal')}>
                {t('disk.col.total')}
              </th>
            </tr>
          </thead>
          <tbody>
            {servers.map((s) => {
              const st = ws.servers?.find((x) => x.id === s.id)
              return (
                <tr key={s.id} className="h-12 border-t border-border">
                  <th scope="row" className="px-4 text-left font-semibold">
                    <span className="flex items-center gap-2.5">
                      <Emblem size={24} icon={st ? iconURL(st) : undefined} name={s.name} />
                      <span className="truncate">{s.name}</span>
                    </span>
                  </th>
                  <td className={cell}>{cellText(bytes(s, 'worlds'))}</td>
                  <td className={cell}>{cellText(bytes(s, 'server_files'))}</td>
                  <td className={cell}>{cellText(bytes(s, 'backups'))}</td>
                  <td className={cell}>{cellText(bytes(s, 'logs'))}</td>
                  <td className={cell}>{cellText(s.total.bytes)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function DiskSkeleton({ phone }: { phone: boolean }) {
  return (
    <div className={cn('flex flex-col', phone ? 'gap-5' : 'gap-6')} aria-busy="true" aria-label={t('common.loading')}>
      <Card className={cn(phone && 'p-4')}>
        <Skeleton className="h-7 w-52" />
        <Skeleton className="mt-3 h-3 w-full rounded-full" />
        <Skeleton className="mt-3 h-4 w-4/5" />
      </Card>
      <div>
        <Skeleton className="h-5 w-40" />
        <div className="mt-3 overflow-hidden rounded-2xl border border-border bg-white">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="flex min-h-[62px] items-center gap-3 border-b border-border px-4 last:border-b-0">
              <span className="flex-1">
                <Skeleton className="h-4 w-56" />
                <Skeleton className="mt-1.5 h-3 w-40" />
              </span>
              <Skeleton className="h-4 w-14" />
              {!phone && <Skeleton className="h-7 w-16 rounded-lg" />}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function ReviewDialog({ way, report, machine, phone, copiesPlace, onClose, onDelete }: { way: DiskWay; report: DiskReport; machine: string; phone: boolean; copiesPlace?: string; onClose: () => void; onDelete: (ids: string[]) => void }) {
  const [off, setOff] = useState<ReadonlySet<string>>(() => new Set())
  const rows = reviewRows(way, report, phone ? 5 : 6, !phone, machine)
  const chosen = rows.filter((r) => !off.has(r.key))
  const count = chosen.reduce((n, r) => n + r.ids.length, 0)
  const bytes = chosen.reduce((n, r) => n + r.bytes, 0)
  const toggle = (key: string, on: boolean) =>
    setOff((s) => {
      const next = new Set(s)
      if (on) next.delete(key)
      else next.add(key)
      return next
    })
  const names = new Map(report.servers.map((s) => [s.id, s.name]))
  const title = way.id === 'set_aside' ? t('disk.reviewSetAside') : wayTitle(way)
  const lead =
    way.id === 'old_backups'
      ? t('disk.reviewBackupsLead', { count: way.candidateIds.length, size: sizeText(way.bytes) })
      : way.id === 'set_aside'
        ? t('disk.reviewSetAsideLead', { size: sizeText(way.bytes) })
        : wayText(way, names)
  const note = copiesPlace ? t('disk.copiesStay', { place: copiesPlace }) : ''
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[560px]" showCloseButton={phone}>
        <DialogHeader className={cn('gap-1.5', phone && 'px-5 pt-4 pr-14')}>
          <DialogTitle className="text-lg leading-6 font-bold">{title}</DialogTitle>
          <DialogDescription className={cn(phone ? 'text-[15px]' : 'text-[13px]')}>{lead}</DialogDescription>
        </DialogHeader>
        <DialogPanel className={cn('pt-2', phone && 'px-5')}>
          <ul className="overflow-hidden rounded-2xl border border-border">
            {rows.map((r) => (
              <li key={r.key} className="border-b border-border last:border-b-0">
                <label className={cn('flex cursor-pointer items-center gap-3 px-3 py-2 hover:bg-accent/40', phone ? 'min-h-12' : 'min-h-10')}>
                  <Checkbox checked={!off.has(r.key)} onCheckedChange={(c) => toggle(r.key, c === true)} />
                  <span className="min-w-0 flex-1">
                    <span className={cn('block', phone ? 'text-base leading-[22px]' : 'text-[13px] leading-5')}>{r.label}</span>
                    {r.hint && <span className="block text-xs text-muted-foreground">{r.hint}</span>}
                  </span>
                  <span className={cn('font-semibold tabular-nums', phone ? 'text-[15px]' : 'text-xs')}>{sizeText(r.bytes)}</span>
                </label>
              </li>
            ))}
          </ul>
          {phone && note && <p className="mt-3 text-[13px] text-muted-foreground">{note}</p>}
        </DialogPanel>
        <DialogFooter variant="bare" className={cn(!phone && 'mx-6 items-center border-t border-border px-0 pt-4 sm:justify-between')}>
          {!phone && <span className="text-xs text-muted-foreground">{note}</span>}
          <div className="flex flex-col-reverse gap-2 sm:flex-row">
            <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" size={phone ? 'touch' : 'default'} disabled={count === 0} onClick={() => onDelete(chosen.flatMap((r) => r.ids))}>
              <Trash2Icon />
              {count ? t('disk.deleteCount', { count, size: sizeText(bytes) }) : t('disk.delete')}
            </Button>
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function ConfirmDialog({ way, names, phone, onClose, onDelete }: { way: DiskWay; names: Map<string, string>; phone: boolean; onClose: () => void; onDelete: () => void }) {
  const size = sizeText(way.bytes)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]" showCloseButton={phone}>
        <DialogHeader className={cn('gap-1.5', phone && 'px-5 pt-4 pr-14')}>
          <DialogTitle className="text-lg leading-6 font-bold">{wayTitle(way)}</DialogTitle>
          <DialogDescription className={cn(phone ? 'text-[15px]' : 'text-[13px]')}>{wayText(way, names)}</DialogDescription>
        </DialogHeader>
        <DialogFooter variant="bare" className={cn(!phone && 'mx-6 border-t border-border px-0 pt-4')}>
          <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" size={phone ? 'touch' : 'default'} onClick={onDelete}>
            <Trash2Icon />
            {way.action === 'clear' ? t('disk.clearSize', { size }) : t('disk.deleteSize', { size })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
