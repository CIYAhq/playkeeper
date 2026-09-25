import { useEffect, useState, type ReactNode } from 'react'
import { CircleArrowUpIcon, RefreshCwIcon, RotateCwIcon, Trash2Icon, XIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { AddonKey, AddonProgress, AddonRemoval, AddonRemovePreview, AddonStep } from '@/api/types'
import { errorText, serverApi } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { JobSteps, type StepState } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogClose, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { checksumFailed, downloadProgress, keyFrom, keyOf, opFiles, opNotice, opRestartNeeded, sameKey } from '@/lib/addons'
import { formatList } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useAddons, type Ask, type Job } from './state'

/** Keeps what a dialog showed while it fades out after closing. */
function useLast<T>(v: T | undefined): T | undefined {
  const [last, setLast] = useState<T>()
  useEffect(() => {
    if (v !== undefined) setLast(v)
  }, [v])
  return v ?? last
}

function fileTitle(f: AddonProgress): string {
  return f.state === 'verified' ? t('addons.downloaded', { name: f.name, version: f.versionNumber }) : t('addons.downloading', { name: f.name, version: f.versionNumber })
}

function fileHint(f: AddonProgress): string {
  // A reinstall brings back the same version: "Was 24.1" would say nothing.
  const was = f.was && f.was !== f.versionNumber ? f.was : undefined
  if (f.state === 'verified') {
    if (f.neededBy) return t('addons.neededByMatched', { name: f.neededBy })
    if (was) return t('addons.wasMatched', { version: was })
    return t('addons.checksumMatched')
  }
  const sizes = downloadProgress(f.received, f.size)
  if (f.neededBy) return t('addons.neededByProgress', { name: f.neededBy, ...sizes })
  if (was) return t('addons.wasProgress', { version: was, ...sizes })
  return t('addons.progress', sizes)
}

const fileState: Record<AddonProgress['state'], StepState> = { waiting: 'todo', downloading: 'current', verified: 'done', failed: 'failed' }

/** A file the plan downloads, before the agent reports on it. */
function planned(s: AddonStep): AddonProgress {
  return { name: s.name, versionNumber: s.versionNumber, was: s.was, neededBy: s.neededBy, size: s.size, received: 0, state: 'waiting' }
}

/** An install or update as it runs, then what's next: a restart, or why nothing was installed. */
export function JobDialog() {
  const a = useAddons()
  const job = useLast(a.job)
  return (
    <Dialog open={!!a.job} onOpenChange={(open) => !open && a.closeJob()}>
      <DialogPopup className="sm:max-w-[540px]" showCloseButton={false}>
        {job && <JobBody job={job} />}
      </DialogPopup>
    </Dialog>
  )
}

function JobBody({ job }: { job: Job }) {
  const a = useAddons()
  const phone = useIsPhone()
  const [restarting, setRestarting] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const op = job.op
  // Until the agent reports its files, the rows are the plan's.
  const reported = op ? opFiles(op) : []
  const files = reported.length > 0 ? reported : (job.plan?.steps ?? []).map(planned)
  const failed = op?.status === 'failed'
  const running = op?.status === 'running'
  const waiting = !op
  const online = a.server.phase === 'online'
  const restartNeeded = !failed && (running || waiting ? online : opRestartNeeded(op))
  const notice = opNotice(op)
  const size = phone ? 'touch' : 'default'

  const steps: Parameters<typeof JobSteps>[0]['steps'] = []
  if (failed) {
    const gone = files.filter((f) => f.state === 'verified' || f.state === 'failed').length
    const removed = gone === 2 ? t('addons.downloadsRemovedBoth') : gone > 0 ? t('addons.downloadsRemoved', { count: gone }) : undefined
    const bad = files.find((f) => f.state === 'failed')
    for (const f of reported) if (f.state === 'verified') steps.push({ title: fileTitle(f), hint: fileHint(f), state: 'done' })
    const title =
      checksumFailed(notice) && (bad || notice?.params?.name)
        ? t('addons.failedChecksum', { name: bad?.name ?? notice?.params?.name ?? '', version: bad?.versionNumber ?? '' })
        : (notice?.message ?? op?.error ?? '')
    steps.push({ title, hint: removed ?? notice?.hint ?? op?.hint, state: 'failed' })
  } else {
    if (files.length === 0 && running) steps.push({ title: t('common.loading'), state: 'current' })
    for (const f of files) {
      const pct = f.size > 0 ? (f.received / f.size) * 100 : 0
      steps.push({ title: fileTitle(f), hint: fileHint(f), state: fileState[f.state], progress: f.state === 'downloading' ? pct : undefined })
    }
    if (restartNeeded) steps.push({ title: t('addons.restartToLoadThem', { server: a.server.name }), state: running || waiting ? 'todo' : 'current' })
  }
  const manual = !op ? (job.plan?.manual ?? []) : Array.isArray(op.detail?.manual) ? (op.detail.manual as { message: string }[]) : []

  const restart = async () => {
    setRestarting(true)
    await a.restart()
    setRestarting(false)
  }
  const confirm = async () => {
    setConfirming(true)
    await job.confirm?.()
    setConfirming(false)
  }

  return (
    <>
      <div className="flex items-center gap-4 px-6 pt-6 pb-2 max-sm:px-5 max-sm:pt-3">
        <Pip pose={failed ? 'hurt' : 'hardhat'} size={52} />
        <DialogTitle className="min-w-0 text-xl leading-7 font-bold">{failed ? t('addons.failedTitle') : job.title}</DialogTitle>
      </div>
      <DialogPanel className="pt-3 max-sm:px-5">
        <div aria-live="polite">
          <JobSteps steps={steps} />
        </div>
        {manual.length > 0 && (
          <ul className="mt-4 flex flex-col gap-1 text-[13px] text-warning-foreground">
            {manual.map((m) => (
              <li key={m.message}>{m.message}</li>
            ))}
          </ul>
        )}
      </DialogPanel>
      <DialogFooter variant="bare" className="border-t border-border pt-4 sm:items-center">
        {running ? (
          <>
            <p className="text-xs text-muted-foreground max-sm:text-center sm:mr-auto">{t('addons.keepsGoing')}</p>
            <Button variant="ghost" size={size} onClick={a.closeJob}>
              {t('common.close')}
            </Button>
            {online && (
              <Button size={size} disabled>
                <RotateCwIcon />
                {t('addons.restartNow')}
              </Button>
            )}
          </>
        ) : failed ? (
          <>
            {checksumFailed(notice) && (
              <a href={t('addons.learnMoreUrl')} target="_blank" rel="noreferrer" className="text-xs font-medium text-success-strong hover:underline max-sm:text-center sm:mr-auto" aria-label={t('common.external', { label: t('addons.learnMore') })}>
                {t('addons.learnMore')}
              </a>
            )}
            <Button variant="ghost" size={size} onClick={a.closeJob}>
              {t('common.close')}
            </Button>
            {notice?.kind === 'plan_changed' && job.lookAgain ? (
              <Button size={size} onClick={job.lookAgain}>
                <RefreshCwIcon />
                {t('addons.lookAgain')}
              </Button>
            ) : (
              <Button size={size} onClick={job.retry}>
                <RefreshCwIcon />
                {t('common.tryAgain')}
              </Button>
            )}
          </>
        ) : waiting ? (
          <>
            <Button variant="ghost" size={size} className="sm:mr-auto" onClick={a.closeJob}>
              {t('common.close')}
            </Button>
            <Button size={size} onClick={() => void confirm()} loading={confirming} disabled={!job.confirm || !!a.server.operation}>
              <CircleArrowUpIcon />
              {t('addons.update')}
            </Button>
          </>
        ) : restartNeeded ? (
          <>
            <Button variant="ghost" size={size} className="sm:mr-auto" onClick={a.closeJob}>
              {t('common.later')}
            </Button>
            <Button size={size} onClick={() => void restart()} loading={restarting} disabled={!!a.server.operation && a.server.operation.id !== op?.id}>
              <RotateCwIcon />
              {t('addons.restartNow')}
            </Button>
          </>
        ) : (
          <Button size={size} onClick={a.closeJob}>
            {t('common.done')}
          </Button>
        )}
      </DialogFooter>
    </>
  )
}

/** Remove, with its settings kept or not, and the dependencies nothing else needs. */
export function RemoveDialog() {
  const a = useAddons()
  const target = useLast(a.removing)
  return (
    <Dialog open={!!a.removing} onOpenChange={(open) => !open && a.askRemove(undefined)}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={false}>
        {target && <RemoveBody key={keyOf(target)} target={target} />}
      </DialogPopup>
    </Dialog>
  )
}

function asApiError(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' })
}

function PhoneClose() {
  return (
    <DialogClose render={<Button variant="ghost" size="icon" className="-mr-2 size-11 shrink-0" aria-label={t('common.close')} />}>
      <XIcon />
    </DialogClose>
  )
}

function RemoveBody({ target }: { target: AddonKey }) {
  const a = useAddons()
  const phone = useIsPhone()
  const [preview, setPreview] = useState<AddonRemovePreview>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)
  const [keep, setKeep] = useState(true)
  const [orphans, setOrphans] = useState<string[]>([])
  const [busy, setBusy] = useState<'remove' | 'force'>()
  const serverId = a.server.id

  useEffect(() => {
    let stale = false
    get<AddonRemovePreview>(serverApi(serverId, `/addons/project/${target.source}/${encodeURIComponent(target.projectId)}/removal`))
      .then((p) => {
        if (stale) return
        setPreview(p)
        setError(undefined)
      })
      .catch((e: unknown) => !stale && setError(asApiError(e)))
    return () => {
      stale = true
    }
  }, [serverId, target, attempt])

  const name = preview?.addon.name ?? a.rows.find((r) => r.addon && sameKey(r.addon, target))?.name ?? ''
  const size = phone ? 'touch' : 'default'
  const neededBy = preview?.neededBy ?? []
  const chosen = (preview?.orphans ?? []).filter((o) => orphans.includes(keyOf(o)))
  const count = 1 + chosen.length
  const removeLabel = count === 1 ? t('addons.removeName', { name }) : t(a.kind === 'mod' ? 'addons.removeManyMods' : 'addons.removeMany', { count })

  const remove = async (force: boolean) => {
    if (!preview) return
    setBusy(force ? 'force' : 'remove')
    try {
      const res = await post<AddonRemoval>(serverApi(serverId, '/addons/remove'), {
        ...keyFrom(target),
        keepConfig: keep && !!preview.configFolder,
        force: force || undefined,
        changed: preview.changed || undefined,
        orphans: chosen.length ? chosen.map(keyFrom) : undefined,
      })
      for (const w of res.warnings) toastManager.add({ title: w.message, description: w.hint, type: 'warning' })
      a.askRemove(undefined)
      await a.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }

  // Desktop puts a checkbox before its text; the phone keeps every control on the right.
  const row = ({ id, control, title, hint, lead, first }: { id: string; control: ReactNode; title: string; hint: string; lead?: boolean; first: boolean }) => (
    <label key={id} htmlFor={id} className={cn('flex cursor-pointer items-center gap-3', phone ? 'min-h-14 px-4 py-2.5' : 'py-3', !first && 'border-t border-border')}>
      {lead && !phone && control}
      <span className="min-w-0 flex-1">
        <span className={cn('block', phone ? 'text-[15px]' : 'text-[13px] font-semibold')}>{title}</span>
        <span className={cn('block text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{hint}</span>
      </span>
      {!(lead && !phone) && control}
    </label>
  )

  const orphanRows = neededBy.length === 0 ? (preview?.orphans ?? []) : []
  const options = preview && (preview.configFolder || orphanRows.length > 0) && (
    <div className={cn(phone ? 'mt-4 overflow-hidden rounded-2xl border border-border' : (neededBy.length > 0 || preview.changed) && 'mt-3 border-t border-border')}>
      {preview.configFolder && row({ id: 'addon-keep', control: <Switch id="addon-keep" checked={keep} onCheckedChange={setKeep} />, title: t('addons.keepSettings'), hint: t('addons.keepSettingsHint'), first: true })}
      {orphanRows.map((o, i) => {
        const k = keyOf(o)
        const id = `addon-orphan-${i}`
        return row({
          id,
          control: <Checkbox id={id} checked={orphans.includes(k)} onCheckedChange={(v) => setOrphans((prev) => (v === true ? [...prev, k] : prev.filter((x) => x !== k)))} />,
          title: t('addons.alsoRemove', { name: o.name, version: o.versionNumber }),
          hint: t('addons.nothingNeeds'),
          lead: true,
          first: !preview.configFolder && i === 0,
        })
      })}
    </div>
  )

  return (
    <>
      <div className="flex items-start gap-3 px-6 pt-6 max-sm:px-5 max-sm:pt-3">
        <div className="min-w-0 flex-1">
          <DialogTitle className="text-xl leading-7 font-bold max-sm:text-lg">{t('addons.removeTitle', { name })}</DialogTitle>
          <DialogDescription className="mt-0.5 text-[13px] max-sm:mt-3 max-sm:text-[15px]">{t('addons.stopsAtRestart')}</DialogDescription>
        </div>
        {phone && <PhoneClose />}
      </div>
      <DialogPanel className="pt-3 max-sm:px-5">
        {error ? (
          <Notice tone="error" title={error.message} action={<Button variant="outline" onClick={() => setAttempt((n) => n + 1)}>{t('common.tryAgain')}</Button>}>
            {error.hint}
          </Notice>
        ) : !preview ? (
          <div className="flex flex-col gap-2 py-2" aria-busy="true">
            <Skeleton className="h-3.5 w-40" />
            <Skeleton className="h-3 w-56" />
          </div>
        ) : (
          <>
            {preview.changed && <p className="text-[13px] font-semibold text-warning-foreground">{t('addons.changed')}</p>}
            {neededBy.length > 0 && (
              <div className={cn(preview.changed && 'mt-2')} role="alert">
                <p className="text-[13px] font-semibold text-warning-foreground">{t('addons.neededBy', { names: formatList(neededBy), count: neededBy.length })}</p>
                <p className="text-xs text-muted-foreground">{t('addons.neededByBody', { names: formatList(neededBy) })}</p>
              </div>
            )}
            {options}
          </>
        )}
      </DialogPanel>
      <DialogFooter variant="bare" className={cn('border-t border-border pt-4', neededBy.length > 0 && 'sm:justify-between')}>
        {neededBy.length > 0 ? (
          <>
            <Button variant="destructive-outline" size={size} onClick={() => void remove(true)} loading={busy === 'force'} disabled={!!busy}>
              {t('addons.removeAnyway')}
            </Button>
            <Button size={size} onClick={() => a.askRemove(undefined)}>
              {t('addons.keep', { name })}
            </Button>
          </>
        ) : (
          <>
            <Button variant={phone ? 'outline' : 'ghost'} size={size} onClick={() => a.askRemove(undefined)}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" size={size} onClick={() => void remove(false)} loading={busy === 'remove'} disabled={!preview || !!busy}>
              <Trash2Icon />
              {removeLabel}
            </Button>
          </>
        )}
      </DialogFooter>
    </>
  )
}

/** Updating a file that changed since it was installed replaces the change, so it asks first. */
export function UpdateAskDialog() {
  const a = useAddons()
  const ask = useLast(a.asking)
  return (
    <Dialog open={!!a.asking} onOpenChange={(open) => !open && a.askUpdate(undefined)}>
      <DialogPopup className="sm:max-w-[460px]" showCloseButton={false}>
        {ask && <AskBody ask={ask} />}
      </DialogPopup>
    </Dialog>
  )
}

function AskBody({ ask }: { ask: Ask }) {
  const a = useAddons()
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const size = phone ? 'touch' : 'default'
  const update = async () => {
    setBusy(true)
    await a.update([ask.key], t('addons.updatingOne', { name: ask.name }), true)
    setBusy(false)
  }
  return (
    <>
      <div className="flex items-start gap-3 px-6 pt-6 max-sm:px-5 max-sm:pt-3">
        <div className="min-w-0 flex-1">
          <DialogTitle className="text-xl leading-7 font-bold max-sm:text-lg">{t('addons.updateAsk', { name: ask.name })}</DialogTitle>
          <DialogDescription className="mt-0.5 text-[13px] font-semibold text-warning-foreground max-sm:mt-3 max-sm:text-[15px]">{t('addons.changed')}</DialogDescription>
        </div>
        {phone && <PhoneClose />}
      </div>
      <DialogFooter variant="bare" className="mt-4 border-t border-border pt-4">
        <Button variant={phone ? 'outline' : 'ghost'} size={size} onClick={() => a.askUpdate(undefined)}>
          {t('common.cancel')}
        </Button>
        <Button size={size} onClick={() => void update()} loading={busy} disabled={!!a.server.operation}>
          {t('addons.updateTo', { version: ask.version })}
        </Button>
      </DialogFooter>
    </>
  )
}
