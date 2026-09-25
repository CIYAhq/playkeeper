import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { CheckIcon, CircleArrowUpIcon, ExternalLinkIcon, XIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { UpdateInfo } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Elapsed, Spinner } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatList } from '@/lib/format'
import { cn } from '@/lib/utils'

/** Reads what Playkeeper knows about updates, while `enabled`. */
export function useUpdateInfo(enabled: boolean) {
  const { machine } = useWorkspace()
  const [info, setInfo] = useState<UpdateInfo>()
  const [error, setError] = useState<string>()
  const id = machine?.id
  useEffect(() => {
    if (!enabled || !id) return
    let cancelled = false
    get<UpdateInfo>(machineApi(id, '/update'))
      .then((i) => !cancelled && setInfo(i))
      .catch((e: unknown) => !cancelled && setError(errorText(e)))
    return () => {
      cancelled = true
    }
  }, [enabled, id])
  return { info, setInfo, error }
}

/** The release notes' bullet points, without Markdown. */
export function noteLines(notes: string | undefined, max = 5): string[] {
  if (!notes) return []
  const plain = (s: string) =>
    s
      .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
      .replace(/[*_`]+/g, '')
      .trim()
  const bullets = notes
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => /^[-*] /.test(l))
    .map((l) => plain(l.slice(2)))
  if (bullets.length) return bullets.slice(0, max)
  const first = notes.split(/\n\s*\n/)[0]
  return first ? [plain(first)] : []
}

/** The sidebar row that offers an update, or shows one being installed. */
export function UpdateRow() {
  const ws = useWorkspace()
  const [open, setOpen] = useState(false)
  const available = ws.machine?.live?.updateAvailable
  if (!ws.updating && !available) return null
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex h-8 w-full items-center gap-2.5 rounded-lg px-2 text-left text-sm font-medium outline-none hover:bg-black/[.035] focus-visible:ring-2 focus-visible:ring-ring"
      >
        {ws.updating ? <Spinner className="size-3.5" /> : <span className="mx-1 size-2 rounded-full bg-success ring-3 ring-success/20" aria-hidden="true" />}
        <span className="min-w-0 flex-1 truncate">{ws.updating ? t('nav.updating') : t('nav.updateAvailable')}</span>
        <span className={cn('text-xs tabular-nums', ws.updating ? 'text-info-foreground' : 'text-muted-foreground')}>{ws.updating ? ws.updatingSince ? <Elapsed since={ws.updatingSince} /> : null : available}</span>
      </button>
      <UpdateDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

export type StepState = 'done' | 'current' | 'todo' | 'failed'

function StepMark({ state }: { state: StepState }) {
  if (state === 'done')
    return (
      <span className="inline-flex size-[22px] shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground">
        <CheckIcon className="size-3.5" aria-hidden="true" />
      </span>
    )
  if (state === 'current') return <Spinner className="m-[3px] size-4 border-2" />
  if (state === 'failed')
    return (
      <span className="inline-flex size-[22px] shrink-0 items-center justify-center rounded-full bg-destructive text-white">
        <XIcon className="size-3.5" aria-hidden="true" />
      </span>
    )
  return <span className="inline-flex size-[22px] shrink-0 rounded-full border-[1.5px] border-dashed border-muted-foreground/50" aria-hidden="true" />
}

/** A list of steps with a mark each; the running one gets a blue bar. */
export function JobSteps({ steps }: { steps: { title: ReactNode; hint?: ReactNode; state: StepState; progress?: number }[] }) {
  const id = useId()
  return (
    <ol className="flex flex-col gap-4">
      {steps.map((s, i) => (
        <li key={i} className="flex gap-3" aria-current={s.state === 'current' ? 'step' : undefined}>
          <StepMark state={s.state} />
          <div className="min-w-0 flex-1">
            <div id={`${id}-${i}`} className={cn('text-sm font-semibold', s.state === 'todo' && 'font-medium text-muted-foreground', s.state === 'failed' && 'text-destructive-foreground')}>
              {s.title}
            </div>
            {s.hint && <div className="mt-0.5 text-xs text-muted-foreground">{s.hint}</div>}
            {s.state === 'current' && s.progress !== undefined && (
              <div className="mt-2 h-1.5 max-w-[280px] overflow-hidden rounded-full bg-foreground/8" role="progressbar" aria-labelledby={`${id}-${i}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(s.progress)}>
                <div className="h-full rounded-full bg-info transition-[width]" style={{ width: `${s.progress}%` }} />
              </div>
            )}
          </div>
        </li>
      ))}
    </ol>
  )
}

function updateSteps(phase: string, disconnected: boolean, version: string, current: string) {
  const order = ['downloading', 'verifying', 'restarting', 'back']
  const at = disconnected ? 2 : Math.max(0, order.indexOf(phase))
  const state = (i: number): StepState => (i < at ? 'done' : i === at ? 'current' : 'todo')
  const progress = [30, 60, 72, 90]
  return [
    { title: t('update.step.download', { version }), hint: t('update.step.downloadHint'), state: state(0), progress: progress[0] },
    { title: t('update.step.verify'), hint: t('update.step.verifyHint'), state: state(1), progress: progress[1] },
    { title: t('update.step.restart'), hint: t('update.step.restartHint'), state: state(2), progress: progress[2] },
    { title: t('update.step.back'), hint: t('update.step.backHint', { current }), state: state(3), progress: progress[3] },
  ]
}

/** Updating Playkeeper itself: what's new, then the install's progress. */
export function UpdateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const { info, error } = useUpdateInfo(open)
  const [busy, setBusy] = useState(false)
  const live = ws.machine?.live
  const lastPhase = useRef('downloading')
  if (live?.operation?.kind === 'update' && live.operation.phase) lastPhase.current = live.operation.phase
  const current = info?.current ?? live?.agentVersion ?? ws.me.version
  const names = (ws.servers ?? []).map((s) => s.name)
  const servers = formatList(names)

  async function apply() {
    if (!info?.latest || !ws.machine) return
    setBusy(true)
    try {
      await post(machineApi(ws.machine.id, '/update/apply'), { version: info.latest })
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const updating = ws.updating
  const target = updating ?? info?.latest ?? live?.updateAvailable ?? ''
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={!updating}>
        <div className="flex items-start gap-4 px-6 pt-6 pb-2 max-sm:px-5">
          <Pip pose={updating ? 'hardhat' : 'box'} size={phone ? 56 : 52} />
          <div className="min-w-0 pt-1">
            <DialogTitle className="text-xl leading-7 font-bold">{updating ? t('update.updatingTitle', { version: target }) : t('update.title', { version: target })}</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px]">
              {updating ? (names.length ? t('update.updatingLead', { servers }) : t('update.updatingLeadNoServers')) : phone ? t('update.metaPhone', { current }) : t('update.meta', { current })}
            </DialogDescription>
          </div>
        </div>
        <DialogPanel className="pt-3 max-sm:px-5">
          {updating ? (
            <JobSteps steps={updateSteps(lastPhase.current, !live, target, current)} />
          ) : (
            <>
              {error && <p className="text-sm text-destructive-foreground">{error}</p>}
              {!info && !error && (
                <>
                  <div className="flex h-5 items-center">
                    <Skeleton className="h-3 w-24" />
                  </div>
                  <ListSkeleton rows={3} lines={1} face="size-1.5 rounded-full" rowClassName="flex h-[18px] items-center gap-2.5 max-sm:h-5" className="mt-2 flex flex-col gap-1.5" />
                </>
              )}
              {info && noteLines(info.notes).length > 0 && (
                <>
                  <h3 className="text-[13px] font-semibold">{t('update.whatsNew')}</h3>
                  <ul className="mt-2 flex flex-col gap-1.5">
                    {noteLines(info.notes).map((l) => (
                      <li key={l} className="flex gap-2.5 text-[13px] leading-[18px] max-sm:text-[15px] max-sm:leading-5">
                        <span className="mt-[7px] size-1.5 shrink-0 rounded-full bg-success" aria-hidden="true" />
                        {l}
                      </li>
                    ))}
                  </ul>
                </>
              )}
              <p className="mt-4 text-xs leading-[18px] text-muted-foreground max-sm:text-[13px]">
                {phone ? t('update.safetyPhone', { current, version: target }) : names.length ? t('update.safety', { servers, version: target, current }) : t('update.safetyNoServers', { version: target, current })}
              </p>
            </>
          )}
        </DialogPanel>
        {updating ? (
          <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
            <span className="text-xs text-muted-foreground">{t('update.closeNote')}</span>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.hide')}
            </Button>
          </DialogFooter>
        ) : phone ? (
          <div className="flex flex-col gap-1 px-5 pt-2">
            <Button size="touch" onClick={apply} loading={busy} disabledReason={info?.available ? undefined : info ? t('update.latest') : t('common.loading')}>
              <CircleArrowUpIcon />
              {t('update.updateNow')}
            </Button>
            <Button variant="ghost" size="touch" onClick={() => onOpenChange(false)}>
              {t('common.later')}
            </Button>
          </div>
        ) : (
          <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
            <a href={t('update.notesUrl', { version: target })} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-[13px] font-medium text-primary hover:underline">
              {t('update.notes')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => onOpenChange(false)}>
                {t('common.later')}
              </Button>
              <Button onClick={apply} loading={busy} disabledReason={info?.available ? undefined : info ? t('update.latest') : t('common.loading')}>
                <CircleArrowUpIcon />
                {t('update.update')}
              </Button>
            </div>
          </DialogFooter>
        )}
      </DialogPopup>
    </Dialog>
  )
}
