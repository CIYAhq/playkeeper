import { useEffect, useRef, useState, type ReactNode } from 'react'
import { CheckIcon, CircleXIcon, CopyIcon, EllipsisIcon, HistoryIcon, InfoIcon, RotateCcwIcon, ShieldCheckIcon, Trash2Icon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Backup, OffsiteCopy, OffsitePending, OffsiteView, Operation, RestorePreview, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { copyText, Spinner } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { jobsOnScreen, restoreCopyHash } from '@/components/app/jobs'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatBytes, formatDate, formatDay, formatPercent } from '@/lib/format'
import { whyNot } from '@/lib/phase'
import { presenceProps, type Presence } from '@/lib/presence'
import { navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

export type StoredRow = { kind: 'here'; backup: Backup; copy?: OffsiteCopy; copying?: number } | { kind: 'there'; copy: OffsiteCopy }

export function useStoredCopies(serverId: string) {
  return usePoll(
    async () => {
      const view = await get<OffsiteView>(serverApi(serverId, '/offsite'))
      const copies = view.configured ? (await get<{ copies: OffsiteCopy[] }>(serverApi(serverId, '/offsite/copies'))).copies : []
      return { view, copies }
    },
    5000,
    serverId,
  )
}

function copyingPercent(p: OffsitePending | undefined, backupId: string): number | undefined {
  if (!p || p.backupId !== backupId || (!p.uploading && p.error)) return undefined
  return p.total > 0 ? Math.floor((p.sent / p.total) * 100) : 0
}

/** Backups here and copies whose backup is gone from here, newest first. */
export function storedRows(backups: Backup[], copies: OffsiteCopy[], view: OffsiteView | undefined): StoredRow[] {
  const byBackup = new Map(copies.map((c) => [c.backupId, c]))
  const pending = view?.enabled ? view.pending : undefined
  const rows: StoredRow[] = backups.map((b) => ({ kind: 'here', backup: b, copy: byBackup.get(b.id), copying: copyingPercent(pending, b.id) }))
  const here = new Set(backups.map((b) => b.id))
  for (const c of copies) if (!c.onHost && !here.has(c.backupId)) rows.push({ kind: 'there', copy: c })
  const made = (r: StoredRow) => Date.parse(r.kind === 'here' ? r.backup.createdAt : r.copy.createdAt)
  return rows.sort((a, b) => made(b) - made(a))
}

/** Who removed a copy's backup from this machine, if known. */
function removedHere(c: OffsiteCopy): string | undefined {
  switch (c.removed) {
    case 'rules':
      return t('world.storedRemoved')
    case 'person':
      return c.removedBy ? t('world.storedDeletedBy', { name: c.removedBy }) : undefined
    case undefined:
      return undefined
    default: {
      const never: never = c.removed
      return never
    }
  }
}

/** The Stored column for a row the copies say something about, else undefined. */
export function storedCell(row: StoredRow, place: string): ReactNode {
  if (row.kind === 'there') {
    const why = removedHere(row.copy)
    return (
      <>
        <span className="block font-medium">{t('world.storedOnlyThere', { place })}</span>
        {why && <span className="block text-xs text-muted-foreground">{why}</span>}
      </>
    )
  }
  if (row.copying !== undefined)
    return (
      <>
        <span className="block font-medium">{t('world.storedCopying')}</span>
        <span className="block text-xs text-muted-foreground">{t('world.storedCopyingPct', { percent: formatPercent(row.copying), place })}</span>
      </>
    )
  if (row.copy) return <span className="block font-medium">{t('world.storedBoth', { place })}</span>
  return undefined
}

/** The phone list's "copying 62%" or "here and on Backblaze B2", else undefined. */
export function phoneStored(row: StoredRow, place: string): string | undefined {
  if (row.kind === 'there') return t('world.phoneOnlyThere', { place })
  if (row.copying !== undefined) return t('world.phoneCopying', { percent: formatPercent(row.copying) })
  return row.copy ? t('world.phoneBoth', { place }) : undefined
}

const kindLabel = (kind: string) => (kind === 'manual' ? t('world.manual') : t('world.rollback'))

/** A backup that is only in the copies: it can be fetched back, checked again there or deleted there. */
export function CopyRow({ server: s, row, state = 'staying', place, onRestore, onChanged }: { server: ServerStatus; row: Extract<StoredRow, { kind: 'there' }>; state?: Presence; place: string; onRestore: () => void; onChanged: () => void }) {
  const ws = useWorkspace()
  const { offline } = useServerMachine(s)
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const c = row.copy
  const when = formatDay(c.createdAt)
  const path = `/offsite/copies/${encodeURIComponent(c.name)}`
  const cantRun = whyNot(s, 'change', offline)
  const cantDelete = can(ws.me, 'backups.copies.manage') ? cantRun : t('world.copyHoldersOnly', { place })
  const mayRestore = can(ws.me, 'backups.restore')
  const mayCheck = can(ws.me, 'backups.make')
  const noChecksum = c.sha256 ? undefined : t('world.noChecksum')

  async function check() {
    try {
      await post<Operation>(serverApi(s.id, `${path}/check`))
      toastManager.add({ title: t('world.copyCheckStarted', { place }), description: t('world.copyCheckStartedBody') })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await del(serverApi(s.id, path))
      setConfirm(false)
      toastManager.add({ title: t('world.copyDeletedToast', { place }), type: 'success' })
      onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <tr {...presenceProps(state)} className="h-12 border-t border-border">
      <td className="px-3 py-2">
        <span className="block font-semibold">{when}</span>
        <span className="block text-xs text-muted-foreground">{kindLabel(c.kind)}</span>
      </td>
      <td className="px-3 text-right tabular-nums">{formatBytes(c.sizeBytes)}</td>
      <td className={cn('px-3 font-medium', c.checkError ? 'text-destructive-foreground' : c.checked ? 'text-success-foreground' : 'text-muted-foreground')} title={c.checkError}>
        {c.checkError ? t('world.failed') : c.checked ? t('world.verified') : t('world.unchecked')}
      </td>
      <td className="px-3">{storedCell(row, place)}</td>
      <td className="px-3">
        <span className="flex items-center justify-end gap-1">
          {mayRestore && (
            <Button size="sm" variant="outline" onClick={onRestore}>
              <RotateCcwIcon />
              {t('world.restoreCopy')}
            </Button>
          )}
          <Menu>
            <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('world.menuFor', { time: when })} />}>
              <EllipsisIcon />
            </MenuTrigger>
            <MenuPopup align="end" className="min-w-60">
              {mayRestore && (
                <MenuItem onClick={onRestore} className="items-start py-1.5">
                  <HistoryIcon className="mt-0.5" />
                  <span>
                    <span className="block">{t('world.restoreThis')}</span>
                    <span className="block text-xs text-muted-foreground">{t('world.restoreThisHint')}</span>
                  </span>
                </MenuItem>
              )}
              {mayCheck && (
                <MenuItem disabled={!!cantRun} title={cantRun} className={cn(cantRun && 'data-disabled:pointer-events-auto')} onClick={() => void check()}>
                  <ShieldCheckIcon />
                  {t('world.checkAgain')}
                </MenuItem>
              )}
              <MenuItem
                disabled={!!noChecksum}
                title={noChecksum}
                className={cn('items-start py-1.5', noChecksum && 'data-disabled:pointer-events-auto')}
                onClick={async () => {
                  const ok = await copyText(c.sha256 ?? '')
                  toastManager.add(ok ? { title: t('world.checksumCopied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
                }}
              >
                <CopyIcon className="mt-0.5" />
                <span>
                  <span className="block">{t('world.copyChecksum')}</span>
                  {c.sha256 && <span className="block font-mono text-xs text-muted-foreground">{t('world.checksum', { short: `${c.sha256.slice(0, 6)}…${c.sha256.slice(-4)}` })}</span>}
                </span>
              </MenuItem>
              <MenuSeparator />
              <MenuItem variant="destructive" disabled={!!cantDelete} title={cantDelete} className={cn(cantDelete && 'data-disabled:pointer-events-auto')} onClick={() => setConfirm(true)}>
                <Trash2Icon />
                {t('world.deleteBackup')}
              </MenuItem>
            </MenuPopup>
          </Menu>
        </span>
        <Dialog open={confirm} onOpenChange={setConfirm}>
          <DialogPopup className="sm:max-w-[460px]">
            <DialogHeader>
              <DialogTitle className="text-lg font-bold">{t('world.deleteTitle')}</DialogTitle>
              <DialogDescription>{t('world.deleteCopyBody', { time: when, place })}</DialogDescription>
            </DialogHeader>
            <DialogFooter variant="bare" className="border-t border-border pt-4">
              <Button variant="ghost" onClick={() => setConfirm(false)}>
                {t('common.cancel')}
              </Button>
              <Button variant="destructive" onClick={remove} loading={busy}>
                <Trash2Icon />
                {t('world.deleteConfirm')}
              </Button>
            </DialogFooter>
          </DialogPopup>
        </Dialog>
      </td>
    </tr>
  )
}

/**
 * Follows a restore from a copy. It opens when one starts here, when the
 * World tab shows while one runs, and again when it finishes, so the result
 * is never missed; the top bar's job pill covers the rest.
 */
export function useCopyRestore(s: ServerStatus, onStaged: (p: RestorePreview) => void) {
  const { machine } = useServerMachine(s)
  const [job, setJob] = useState<Operation>()
  const [started, setStarted] = useState<{ id: string; name: string }>()
  const [hidden, setHidden] = useState<string>()
  const [loadError, setLoadError] = useState<string>()
  const [cancelling, setCancelling] = useState<string>()
  const fetched = useRef<string | undefined>(undefined)
  const running = s.operation?.kind === 'offsite-restore' ? s.operation : undefined
  const back = window.location.hash === restoreCopyHash && s.lastOperation?.kind === 'offsite-restore' ? s.lastOperation : undefined
  const adopt = running ?? back
  if (adopt && adopt.id !== job?.id) {
    setJob(adopt)
    setHidden(undefined)
    setLoadError(undefined)
  }
  const live = job && (s.operation?.id === job.id ? s.operation : s.lastOperation?.id === job.id ? s.lastOperation : undefined)
  if (live && live !== job) setJob(live)
  const op = live ?? job
  const name = typeof op?.detail?.name === 'string' ? op.detail.name : started && started.id === op?.id ? started.name : undefined
  const open = !!op && hidden !== `${op.id}:${op.status}`
  const restoreId = op?.status === 'succeeded' && typeof op.detail?.restoreId === 'string' ? op.detail.restoreId : undefined
  const mid = machine?.id

  useEffect(() => {
    if (back) navigate(window.location.pathname, true)
  }, [back])

  const cancelled = op?.status === 'cancelled' ? op.id : undefined
  useEffect(() => {
    if (!cancelled) return
    setHidden(`${cancelled}:cancelled`)
    toastManager.add({ title: t('offsiteRestore.cancelled'), description: t('offsiteRestore.nothingChanged') })
  }, [cancelled])

  const opId = op?.id
  useEffect(() => {
    if (!opId) return
    jobsOnScreen.add(opId)
    return () => {
      jobsOnScreen.delete(opId)
    }
  }, [opId])

  useEffect(() => {
    if (!open || !restoreId || !mid || !opId || fetched.current === restoreId) return
    fetched.current = restoreId
    void (async () => {
      try {
        const p = await get<RestorePreview>(machineApi(mid, `/restore/${restoreId}`))
        setHidden(`${opId}:succeeded`)
        onStaged(p)
      } catch (e) {
        setLoadError(errorText(e))
      }
    })()
  }, [open, restoreId, mid, opId, onStaged])

  async function start(copy: OffsiteCopy) {
    try {
      const o = await post<Operation>(serverApi(s.id, '/offsite/restore'), { name: copy.name })
      setStarted({ id: o.id, name: copy.name })
      setJob(o)
      setHidden(undefined)
      setLoadError(undefined)
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  async function cancel() {
    if (!op) return
    setCancelling(op.id)
    try {
      await post<Operation>(serverApi(s.id, '/offsite/restore/cancel'), { operationId: op.id })
    } catch (e) {
      setCancelling(undefined)
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  return { op, name, open, loadError, start, cancel, cancelling: !!op && cancelling === op.id, hide: () => op && setHidden(`${op.id}:${op.status}`) }
}

type StepState = 'done' | 'active' | 'waiting' | 'failed'

function Step({ state, title, hint, bar }: { state: StepState; title: string; hint?: string; bar?: boolean }) {
  return (
    <li className="flex gap-3">
      <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center" aria-hidden="true">
        {state === 'done' && (
          <span className="flex size-5 items-center justify-center rounded-full bg-primary text-primary-foreground">
            <CheckIcon className="size-3" strokeWidth={3} />
          </span>
        )}
        {state === 'active' && <Spinner className="size-4 border-2" />}
        {state === 'waiting' && <span className="size-5 rounded-full border border-dashed border-muted-foreground/60" />}
        {state === 'failed' && <CircleXIcon className="size-5 text-destructive-foreground" />}
      </span>
      <span className="min-w-0 flex-1">
        <span className={cn('block text-sm', state === 'active' && 'font-medium', state === 'failed' && 'font-medium text-destructive-foreground')}>{title}</span>
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
        {bar && (
          <span className="mt-2 block h-1.5 max-w-72 overflow-hidden rounded-full bg-info/15" role="progressbar" aria-label={title}>
            <span className="progress-indeterminate block h-full w-2/5 rounded-full bg-info" />
          </span>
        )}
      </span>
    </li>
  )
}

export function CopyRestoreDialog({ restore, copies, place }: { restore: ReturnType<typeof useCopyRestore>; copies: OffsiteCopy[]; place: string }) {
  const { op, name } = restore
  if (!op) return null
  const copy = copies.find((c) => c.name === name)
  return <CopyJobDialog op={op} open={restore.open} loadError={restore.loadError} onHide={restore.hide} onCancel={restore.cancel} cancelling={restore.cancelling} date={copy?.createdAt} sizeBytes={copy && (copy.copySizeBytes || copy.sizeBytes)} place={place} />
}

/**
 * Download, decrypt and check a copy, then the usual look at what's inside.
 * With onCancel, the job can be cancelled while it runs; closing only hides it.
 */
export function CopyJobDialog({ op, open, loadError, onHide, onCancel, cancelling, date, sizeBytes, place, background = t('offsiteRestore.background') }: { op: Operation; open: boolean; loadError?: string; onHide: () => void; onCancel?: () => void; cancelling?: boolean; date?: string; sizeBytes?: number; place: string; background?: string }) {
  const phone = useIsPhone()
  const size = sizeBytes ? formatBytes(sizeBytes) : ''
  const failed = op.status === 'failed' || !!loadError
  const checking = op.phase === 'checking' || op.status === 'succeeded'
  const downloadState: StepState = checking ? 'done' : failed ? 'failed' : 'active'
  const checkState: StepState = op.status === 'succeeded' ? 'done' : !checking ? 'waiting' : failed ? 'failed' : 'active'
  const title = date ? t('offsiteRestore.title', { date: formatDate(date) }) : t('offsiteRestore.titleAny')
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onHide()}>
      <DialogPopup className="sm:max-w-[540px]" showCloseButton={phone}>
        <div className="flex items-center gap-3 px-6 pt-6">
          <Pip pose={failed ? 'hurt' : 'hardhat'} size={52} />
          <div className="min-w-0">
            <DialogTitle className="text-lg font-bold">{failed ? t('offsiteRestore.failed') : title}</DialogTitle>
            <DialogDescription>{failed ? (loadError ?? op.error) : t('offsiteRestore.body')}</DialogDescription>
          </div>
        </div>
        <DialogPanel>
          <ol className="flex flex-col gap-3.5">
            <Step state={downloadState} title={(downloadState === 'done' ? t('offsiteRestore.downloaded', { size }) : t('offsiteRestore.downloading', { size })).trim()} hint={t('offsiteRestore.downloadHint', { place })} bar={downloadState === 'active'} />
            <Step state={checkState} title={checkState === 'done' ? t('offsiteRestore.checked') : t('offsiteRestore.checking')} bar={checkState === 'active'} />
            <Step state={op.status === 'succeeded' && !loadError ? 'active' : 'waiting'} title={t('offsiteRestore.inside')} hint={t('offsiteRestore.insideHint')} />
          </ol>
          {failed && !loadError && op.hint && <p className="mt-4 text-[13px] text-muted-foreground">{op.hint}</p>}
        </DialogPanel>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4 max-sm:flex-col max-sm:border-t-0 sm:items-center sm:justify-between">
          {!failed && (
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <InfoIcon className="size-3.5 shrink-0" aria-hidden="true" />
              {background}
            </p>
          )}
          {onCancel && op.status === 'running' ? (
            <Button variant="outline" size={phone ? 'touch' : 'default'} loading={cancelling} onClick={onCancel}>
              {t('common.cancel')}
            </Button>
          ) : (
            <Button variant="outline" size={phone ? 'touch' : 'default'} className={cn(failed && 'sm:ml-auto')} onClick={onHide}>
              {t('common.close')}
            </Button>
          )}
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
