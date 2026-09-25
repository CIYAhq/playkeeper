import type { ReactNode } from 'react'
import { ArchiveIcon, ArrowRightIcon, CheckIcon, ChevronRightIcon, DownloadIcon, LockIcon, PlusIcon, UserPlusIcon, XIcon } from 'lucide-react'
import type { ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Progress, SectionLabel } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { checklist, complete, progress, type Step, type StepId } from '@/lib/checklist'
import { relativeTime } from '@/lib/format'
import { isSettingUp } from '@/lib/phase'
import { linkProps, type Route } from '@/lib/router'
import { cn } from '@/lib/utils'

export function hiddenKey(server: ServerStatus | undefined): string {
  return server ? `firstSteps.hidden.${server.id}` : 'firstSteps.hidden'
}

/** The server the "Get started" steps are about on a page: the open one, or the first with steps left. */
export function checklistServer(servers: ServerStatus[], route: Route): ServerStatus | undefined {
  if (route.name === 'server') return servers.find((s) => s.slug === route.slug)
  return servers.find((s) => !complete(checklist(s))) ?? servers[0]
}

export function stepTitle(id: StepId, short = false): string {
  switch (id) {
    case 'create':
      return t('checklist.create')
    case 'invite':
      return t('checklist.invite')
    case 'joined':
      return t('checklist.joined')
    case 'backup':
      return short ? t('checklist.backup') : t('checklist.backupFirst')
    case 'download':
      return t('checklist.download')
    default: {
      const unreachable: never = id
      return unreachable
    }
  }
}

function stepHint(step: Step, server: ServerStatus | undefined): string {
  const fs = server?.firstSteps
  switch (step.id) {
    case 'create':
      return t('checklist.createHint')
    case 'invite':
      return step.state === 'done' && fs?.invited ? t('checklist.inviteDone', { name: fs.invited }) : t('checklist.inviteHint')
    case 'joined':
      return step.state === 'done' && fs?.friendJoined ? t('checklist.joinedDone', { name: fs.friendJoined, time: relativeTime(fs.friendJoinedAt) }) : t('checklist.joinedHint')
    case 'backup':
      return t('checklist.backupHintLong')
    case 'download':
      return step.state === 'locked' ? t('checklist.downloadLocked') : t('checklist.downloadHint')
    default: {
      const unreachable: never = step.id
      return unreachable
    }
  }
}

/** Where a step is done. */
export function stepRoute(id: StepId, server: ServerStatus | undefined): Route {
  if (!server || id === 'create') return { name: 'new-server' }
  switch (id) {
    case 'invite':
    case 'joined':
      return { name: 'server', slug: server.slug, tab: 'players' }
    case 'backup':
    case 'download':
      return { name: 'server', slug: server.slug, tab: 'world' }
    default: {
      const unreachable: never = id
      return unreachable
    }
  }
}

function stepIcon(id: StepId): ReactNode {
  switch (id) {
    case 'create':
      return <PlusIcon />
    case 'invite':
    case 'joined':
      return <UserPlusIcon />
    case 'backup':
      return <ArchiveIcon />
    case 'download':
      return <DownloadIcon />
    default: {
      const unreachable: never = id
      return unreachable
    }
  }
}

function stepAction(id: StepId): string {
  switch (id) {
    case 'create':
      return t('checklist.createServer')
    case 'invite':
    case 'joined':
      return t('checklist.addPlayer')
    case 'backup':
      return t('checklist.backUpNow')
    case 'download':
      return t('checklist.downloadNow')
    default: {
      const unreachable: never = id
      return unreachable
    }
  }
}

/** The small "Get started" card at the bottom of the sidebar. */
export function GetStartedCard({ route, className }: { route: Route; className?: string }) {
  const { servers, prefs } = useWorkspace()
  if (!servers) return null
  const server = checklistServer(servers, route)
  if (server && isSettingUp(server)) return null
  const steps = checklist(server)
  if (complete(steps) || prefs[hiddenKey(server)] === '1') return null
  const p = progress(steps)
  return (
    <div className={cn('rounded-2xl border border-border bg-white p-3 shadow-card', className)}>
      <div className="flex items-baseline justify-between text-[13px]">
        <span className="font-semibold">{t('checklist.title')}</span>
        <span className="text-xs text-muted-foreground tabular-nums">{t('checklist.progress', { done: p.done, total: p.total })}</span>
      </div>
      <Progress value={(p.done / p.total) * 100} className="mt-2" label={t('checklist.progressDone', { done: p.done, total: p.total })} />
      {p.next && (
        <a {...linkProps(stepRoute(p.next.id, server))} className="mt-2.5 flex items-center gap-1.5 text-xs font-medium text-primary hover:underline">
          <ArrowRightIcon className="size-3.5" aria-hidden="true" />
          {t('checklist.next', { step: stepTitle(p.next.id) })}
        </a>
      )}
    </div>
  )
}

/** A step's action: goes to the right tab, where the step is done. */
function StepButton({ step, server, size = 'sm', className, onBackup }: { step: Step; server: ServerStatus | undefined; size?: 'sm' | 'touch'; className?: string; onBackup?: () => void }) {
  const icon = stepIcon(step.id)
  if (step.id === 'backup' && onBackup) {
    return (
      <Button size={size} className={className} onClick={onBackup}>
        {icon}
        {stepAction(step.id)}
      </Button>
    )
  }
  return (
    <Button size={size} className={className} render={<a {...linkProps(stepRoute(step.id, server))} />}>
      {icon}
      {stepAction(step.id)}
    </Button>
  )
}

function Tile({ step, index, server, onBackup }: { step: Step; index: number; server: ServerStatus; onBackup?: () => void }) {
  const done = step.state === 'done'
  const next = step.state === 'next'
  const locked = step.state === 'locked'
  return (
    <li
      className={cn(
        'flex min-h-[148px] flex-col rounded-2xl border p-3.5',
        next ? 'border-primary/55 bg-white shadow-selected' : 'border-border bg-white',
        locked && 'border-transparent bg-muted/80 text-muted-foreground',
      )}
    >
      <div className="flex items-center justify-between">
        <span
          className={cn(
            'inline-flex size-[22px] items-center justify-center rounded-full text-[11px] font-semibold',
            done && 'bg-primary text-primary-foreground',
            next && 'border-[1.5px] border-primary text-primary',
            step.state === 'todo' && 'bg-muted text-muted-foreground',
          )}
          aria-hidden="true"
        >
          {done ? <CheckIcon className="size-3.5" /> : locked ? <LockIcon className="size-3.5" /> : index + 1}
        </span>
        {done && <span className="text-xs font-medium text-success-foreground">{t('checklist.done')}</span>}
        {next && <span className="text-xs font-medium text-success-foreground">{t('checklist.nextMarker')}</span>}
      </div>
      <h3 className={cn('mt-2.5 text-[13px] font-semibold', locked && 'text-muted-foreground')}>{stepTitle(step.id)}</h3>
      <p className="mt-0.5 text-xs leading-4 text-muted-foreground">{stepHint(step, server)}</p>
      {next && <StepButton step={step} server={server} className="mt-auto w-full" onBackup={onBackup} />}
    </li>
  )
}

/** Overview's "First steps" card: what's done, what's next, one button for it. */
export function FirstStepsCard({ server, phone, onBackup }: { server: ServerStatus; phone?: boolean; onBackup?: () => void }) {
  const { prefs, setPrefs } = useWorkspace()
  const steps = checklist(server)
  if (complete(steps) || prefs[hiddenKey(server)] === '1') return null
  const p = progress(steps)
  const left = p.total - p.done
  const hide = () => void setPrefs({ [hiddenKey(server)]: '1' })
  if (phone) {
    const next = p.next
    return (
      <section aria-label={t('checklist.label')} className="rounded-3xl border border-border bg-warm p-4">
        <div className="flex items-center justify-between">
          <SectionLabel>{t('checklist.label')}</SectionLabel>
          <span className="text-[13px] text-muted-foreground tabular-nums">{t('checklist.progress', { done: p.done, total: p.total })}</span>
        </div>
        <Progress value={(p.done / p.total) * 100} className="mt-2.5" label={t('checklist.progressDone', { done: p.done, total: p.total })} />
        {next && (
          <>
            <div className="mt-4 flex items-start gap-3">
              <Pip pose="letter" size={52} />
              <div className="min-w-0">
                <h2 className="text-[17px] leading-[22px] font-semibold">{stepTitle(next.id)}</h2>
                <p className="mt-1 text-[15px] leading-5 text-muted-foreground">{next.id === 'backup' ? t('checklist.backupPhone') : stepHint(next, server)}</p>
              </div>
            </div>
            <StepButton step={next} server={server} size="touch" className="mt-4 w-full" onBackup={onBackup} />
          </>
        )}
      </section>
    )
  }
  return (
    <section aria-labelledby="first-steps" className="rounded-3xl border border-border bg-warm p-5">
      <div className="flex items-center justify-between">
        <SectionLabel>
          <span id="first-steps">{t('checklist.label')}</span>
        </SectionLabel>
        <Button variant="ghost" size="xs" onClick={hide} aria-label={t('checklist.hideAria')}>
          {t('common.hide')}
          <XIcon />
        </Button>
      </div>
      <div className="mt-3 grid gap-4 lg:grid-cols-[minmax(200px,0.8fr)_2.2fr]">
        <div className="flex flex-col">
          <Pip pose="letter" size={64} />
          <h2 className="mt-3 text-[17px] leading-6 font-bold">{p.done === 0 ? t('checklist.headlineStart', { server: server.name }) : t('checklist.headline', { server: server.name, count: left })}</h2>
          <p className="mt-1 text-[13px] leading-[18px] text-muted-foreground">{t('checklist.body')}</p>
          <div className="mt-auto flex items-center gap-3 pt-4">
            <Progress value={(p.done / p.total) * 100} className="max-w-[120px]" label={t('checklist.progressDone', { done: p.done, total: p.total })} />
            <span className="text-xs text-muted-foreground tabular-nums">{t('checklist.progressDone', { done: p.done, total: p.total })}</span>
          </div>
        </div>
        <ol className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {steps.map((s, i) => (
            <Tile key={s.id} step={s} index={i} server={server} onBackup={onBackup} />
          ))}
        </ol>
      </div>
    </section>
  )
}

/** Home with no servers: the five steps, creating a server first. */
export function EmptySteps({ phone }: { phone?: boolean }) {
  const steps = checklist(undefined)
  const p = progress(steps)
  if (phone) {
    return (
      <section aria-labelledby="get-started" className="mt-6 w-full">
        <SectionLabel className="px-4">
          <span id="get-started">{t('checklist.title')}</span>
          {t('common.dot')}
          {t('checklist.progress', { done: p.done, total: p.total })}
        </SectionLabel>
        <ol className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
          {steps.map((s, i) => {
            const next = s.state === 'next'
            const body = (
              <>
                <span className={cn('w-5 text-center text-[15px] font-semibold', next ? 'text-primary' : 'text-muted-foreground')}>{i + 1}</span>
                <span className="min-w-0 flex-1">
                  <span className="block text-base">{stepTitle(s.id, true)}</span>
                  {next && <span className="block text-[13px] text-muted-foreground">{t('home.nextAbout')}</span>}
                </span>
                {next && <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
              </>
            )
            return (
              <li key={s.id} className="border-b border-border last:border-b-0">
                {next ? (
                  <a {...linkProps({ name: 'new-server' })} className="flex min-h-14 items-center gap-4 px-4 py-2">
                    {body}
                  </a>
                ) : (
                  <div className="flex min-h-12 items-center gap-4 px-4 py-2">{body}</div>
                )}
              </li>
            )
          })}
        </ol>
      </section>
    )
  }
  return (
    <section aria-labelledby="get-started" className="mt-8 w-full max-w-[720px] border-t border-border pt-5">
      <SectionLabel>
        <span id="get-started">{t('checklist.title')}</span>
      </SectionLabel>
      <ol className="mt-3 grid grid-cols-5 gap-4">
        {steps.map((s, i) => (
          <li key={s.id}>
            <div className={cn('text-xs font-medium', s.state === 'next' ? 'text-primary' : 'text-muted-foreground')}>{s.state === 'next' ? t('checklist.stepNext', { n: i + 1 }) : t('checklist.stepNumber', { n: i + 1 })}</div>
            <div className="mt-1 text-[13px] font-semibold">{s.id === 'create' ? t('checklist.create') : stepTitle(s.id, true)}</div>
            <div className="mt-0.5 text-xs text-muted-foreground">{emptyHint(s.id)}</div>
          </li>
        ))}
      </ol>
    </section>
  )
}

function emptyHint(id: StepId): string {
  switch (id) {
    case 'create':
      return t('checklist.createHint')
    case 'invite':
      return t('checklist.inviteHint')
    case 'joined':
      return t('checklist.joinedHint')
    case 'backup':
      return t('checklist.backupHint')
    case 'download':
      return t('checklist.downloadHint')
    default: {
      const unreachable: never = id
      return unreachable
    }
  }
}
