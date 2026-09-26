import { useEffect, useState, type ReactNode } from 'react'
import { ArchiveIcon, ChevronRightIcon, EllipsisIcon, MessageSquareIcon, PencilIcon, PlusIcon, RotateCwIcon, SquareTerminalIcon, Trash2Icon } from 'lucide-react'
import { ApiError, del, get, post } from '@/api/client'
import type { Schedule, ScheduleKind, SchedulePayload, SchedulePreview, ScheduleRun, SchedulesResponse, ScheduleTiming, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, SectionLabel } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect, useIsPhone, type Choice } from '@/components/app/controls'
import { PhoneBackHeader } from '@/components/app/shell'
import { InlineSkeleton, ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { formatBytes, formatClock, formatDate, formatDuration, formatList, relativeTime } from '@/lib/format'
import { presenceProps, useListPresence, type Presence } from '@/lib/presence'
import { linkProps } from '@/lib/router'
import { cn } from '@/lib/utils'
import { usePoll } from '@/lib/usePoll'
import { dayName, past, runDay, sentence, upcoming, upcomingAt, viewerTimeZone, weekdays, zoneLabel } from '@/lib/when'

// Wave 7: what a server does by itself. The Settings section, its phone page
// and the dialog that makes or changes a schedule.

export function useSchedules(serverId: string) {
  return usePoll(() => get<SchedulesResponse>(serverApi(serverId, '/schedules')), 15_000, serverId)
}

function kindIcon(kind: ScheduleKind): ReactNode {
  switch (kind) {
    case 'restart':
      return <RotateCwIcon />
    case 'backup':
      return <ArchiveIcon />
    case 'command':
      return <SquareTerminalIcon />
    case 'announcement':
      return <MessageSquareIcon />
    default: {
      const unreachable: never = kind
      return unreachable
    }
  }
}

/** When a schedule runs, in the viewer's time zone: "every day at 05:00". */
function whenPhrase(timing: ScheduleTiming, nextRun: string | undefined): string {
  // Saved in another time zone, the next run says what the clock here shows.
  const at = timing.at && timing.timeZone !== viewerTimeZone() && nextRun ? formatClock(nextRun) : (timing.at ?? '')
  switch (timing.kind) {
    case 'daily':
      return t('schedules.when.daily', { at })
    case 'weekly': {
      const days = weekdays.filter((d) => timing.days?.includes(d))
      if (days.length === 7) return t('schedules.when.daily', { at })
      if (days.length === 5 && !days.includes('sat') && !days.includes('sun')) return t('schedules.when.weekdays', { at })
      if (days.length === 2 && days.includes('sat') && days.includes('sun')) return t('schedules.when.weekends', { at })
      return t('schedules.when.weekly', { days: formatList(days.map((d) => dayName(d))), at })
    }
    case 'interval':
      return timing.everyHours === 1 ? t('schedules.when.hourly') : t('schedules.when.interval', { count: timing.everyHours ?? 0 })
    case 'once':
      return t('schedules.when.once', { date: nextRun ? formatDate(nextRun) : (timing.date ?? ''), at })
    case 'cron':
      return t('schedules.when.cron', { cron: timing.cron ?? '' })
    default: {
      const unreachable: never = timing.kind
      return unreachable
    }
  }
}

/** A schedule as one plain sentence: "Restart every day at 05:00". */
export function scheduleSentence(s: Pick<Schedule, 'kind' | 'timing' | 'payload' | 'nextRun'>): string {
  const when = whenPhrase(s.timing, s.nextRun)
  switch (s.kind) {
    case 'restart':
      return t('schedules.sentence.restart', { when })
    case 'backup':
      return t('schedules.sentence.backup', { when })
    case 'command':
      return t('schedules.sentence.command', { when: sentence(when), command: s.payload.command ?? '' })
    case 'announcement':
      return t('schedules.sentence.announcement', { when: sentence(when), message: s.payload.message ?? '' })
    default: {
      const unreachable: never = s.kind
      return unreachable
    }
  }
}

/** "Warns players in chat 10, 5 and 1 minute before". */
function warningText(seconds: number[] | undefined): string {
  const list = [...(seconds ?? [])].sort((a, b) => b - a)
  if (list.length === 0) return t('schedules.noWarning')
  const mins = list.filter((s) => s >= 60).map((s) => Math.round(s / 60))
  const secs = list.filter((s) => s < 60)
  const parts: string[] = []
  if (mins.length) parts.push(t('schedules.warnMinutes', { list: formatList(mins.map(String)), count: mins[mins.length - 1] ?? 0 }))
  if (secs.length) parts.push(t('schedules.warnSeconds', { list: formatList(secs.map(String)), count: secs[secs.length - 1] ?? 0 }))
  return t('schedules.warns', { list: parts.join(t('schedules.listSep')) })
}

const reasonKeys: Record<string, MessageKey> = {
  server_stopped: 'schedules.reason.server_stopped',
  server_sleeping: 'schedules.reason.server_sleeping',
  nobody_online: 'schedules.reason.nobody_online',
  agent_down: 'schedules.reason.agent_down',
  late: 'schedules.reason.late',
  busy: 'schedules.reason.busy',
  rejected: 'schedules.reason.rejected',
  error: 'schedules.reason.error',
  interrupted: 'schedules.reason.interrupted',
  invalid: 'schedules.reason.invalid',
  turned_off: 'schedules.reason.turned_off',
  people_playing: 'schedules.reason.people_playing',
  nobody_played: 'schedules.reason.nobody_played',
}

function reasonText(reason: string | undefined, result: string): string {
  const key = reason ? reasonKeys[reason] : undefined
  if (key) return t(key)
  return result === 'missed' ? t('schedules.result.missed') : t('schedules.result.skipped')
}

/** How the last run went, as a word or two: "worked", "skipped: people were playing". */
function lastRunText(s: Schedule): string {
  const r = s.lastRun
  if (!r) return ''
  switch (r.result) {
    case 'succeeded':
      return t('schedules.result.worked')
    case 'running':
      return t('schedules.result.running')
    case 'failed':
      return t('schedules.result.failed')
    case 'skipped':
    case 'missed':
      return reasonText(r.reason, r.result)
    default: {
      const unreachable: never = r.result
      return unreachable
    }
  }
}

function pausedText(s: Schedule, me: string): string {
  return s.updatedBy === me ? t('schedules.pausedYou', { time: relativeTime(s.updatedAt) }) : t('schedules.pausedBy', { actor: s.updatedBy, time: relativeTime(s.updatedAt) })
}

/** The line under a schedule: what it does besides, then when it runs next or how it last went. */
function scheduleHint(s: Schedule, current: SchedulesResponse['current'], me: string, phone: boolean): string {
  if (!s.enabled) return phone ? sentence(pausedText(s, me)) : t('schedules.turnedOff', { paused: pausedText(s, me) })
  const dot = t('common.dot')
  let status: string
  if (current?.scheduleId === s.id) status = current.restartAt ? t('schedules.restartsAt', { time: formatClock(current.restartAt) }) : t('schedules.result.running')
  else if (s.lastRun?.retryAt && new Date(s.lastRun.retryAt).getTime() > Date.now()) status = t('schedules.retryAt', { time: formatClock(s.lastRun.retryAt) })
  else if (s.kind === 'command' && s.lastRun && s.lastRun.result !== 'running') status = phone ? lastRunText(s) : t('schedules.lastRan', { when: past(s.lastRun.due), result: lastRunText(s) })
  else status = s.nextRun ? t('schedules.next', { when: upcoming(s.nextRun) }) : t('schedules.noNext')
  if (phone) return sentence(status)
  const what = s.kind === 'restart' ? warningText(s.payload.warnSeconds) : s.kind === 'backup' ? t('schedules.byRules') : ''
  return what ? `${what}${dot}${status}` : sentence(status)
}

async function setEnabled(server: ServerStatus, s: Schedule, enabled: boolean): Promise<boolean> {
  try {
    await post(serverApi(server.id, `/schedules/${s.id}`), { enabled })
    return true
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
    return false
  }
}

const rowClass = (phone: boolean) => cn('flex items-center gap-3 border-b border-border last:border-b-0', phone ? 'min-h-16 py-3' : 'py-3')

function ScheduleRow({ server, schedule: s, state, current, phone, onEdit, onChanged }: { server: ServerStatus; schedule: Schedule; state: Presence; current: SchedulesResponse['current']; phone: boolean; onEdit: () => void; onChanged: () => void }) {
  const { me } = useWorkspace()
  const [on, setOn] = useState(s.enabled)
  useEffect(() => setOn(s.enabled), [s.enabled])
  const title = scheduleSentence(s)
  const toggle = async (next: boolean) => {
    setOn(next)
    if (!(await setEnabled(server, s, next))) setOn(!next)
    onChanged()
  }
  const remove = async () => {
    try {
      await del(serverApi(server.id, `/schedules/${s.id}`))
      toastManager.add({ title: t('schedules.deleted'), type: 'success' })
      onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }
  const cantChange = formOf(s) === undefined ? t('schedules.cantChange') : undefined
  const view = { ...s, enabled: on }
  return (
    <li {...presenceProps(state)} className={rowClass(phone)}>
      <span className={cn('shrink-0 text-muted-foreground [&_svg]:size-4', phone && '[&_svg]:size-5')} aria-hidden="true">
        {kindIcon(s.kind)}
      </span>
      <button type="button" onClick={cantChange ? undefined : onEdit} disabled={!!cantChange} title={cantChange} className={cn('min-w-0 flex-1 text-left', cantChange && 'cursor-default')}>
        <span className={cn('block font-semibold', phone ? 'text-base font-normal' : 'text-sm', !on && 'text-muted-foreground')}>{title}</span>
        <span className={cn('mt-0.5 block text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{scheduleHint(view, current, me.user.username, phone)}</span>
      </button>
      <Switch checked={on} onCheckedChange={(c) => void toggle(c)} aria-label={t('schedules.switch', { schedule: title })} />
      {!phone && (
        <Menu>
          <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('schedules.menu', { schedule: title })} />}>
            <EllipsisIcon />
          </MenuTrigger>
          <MenuPopup align="end" className="min-w-44">
            <MenuItem disabled={!!cantChange} title={cantChange} className={cn(cantChange && 'data-disabled:pointer-events-auto')} onClick={onEdit}>
              <PencilIcon />
              {t('schedules.change')}
            </MenuItem>
            <MenuSeparator />
            <MenuItem variant="destructive" onClick={() => void remove()}>
              <Trash2Icon />
              {t('schedules.delete')}
            </MenuItem>
          </MenuPopup>
        </Menu>
      )}
    </li>
  )
}

function RowsSkeleton({ phone }: { phone: boolean }) {
  return <ListSkeleton rowClassName={rowClass(phone)} face="size-4 rounded" trailing={<Skeleton className="h-5 w-9 rounded-full" />} />
}

/** Why New schedule can't be pressed yet. */
function newReason(stale: boolean, items: Schedule[] | undefined): string | undefined {
  if (stale) return t('reason.noAgent')
  return items ? undefined : t('common.loading')
}

/** Server settings' Schedules section, then Recent runs. */
export function SchedulesSection({ server }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const list = useSchedules(server.id)
  const runs = usePoll(() => get<{ runs: ScheduleRun[] }>(serverApi(server.id, '/schedules/runs?limit=6')), 30_000, server.id)
  const [editing, setEditing] = useState<Schedule | 'new'>()
  const changed = () => void Promise.all([list.refresh(), runs.refresh()])
  const items = list.data?.schedules
  const rows = useListPresence(items, (s) => s.id)
  return (
    <>
      <Card as="section" id="schedules" aria-labelledby="schedules-title" className="scroll-mt-4 pb-2">
        <div className="flex items-start gap-4">
          <div className="min-w-0 flex-1">
            <CardTitle id="schedules-title">{t('schedules.title')}</CardTitle>
            <CardHint>{t('schedules.zone', { zone: zoneLabel() })}</CardHint>
          </div>
          <Button onClick={() => setEditing('new')} disabledReason={newReason(ws.stale, items)}>
            <PlusIcon />
            {t('schedules.new')}
          </Button>
        </div>
        <div className="mt-2">
          {list.error && !items ? (
            <p className="py-3 text-[13px] text-destructive-foreground">{list.error.message}</p>
          ) : !items ? (
            <RowsSkeleton phone={false} />
          ) : rows.length === 0 ? (
            <p className="animate-fade py-3 text-[13px] text-muted-foreground">{t('schedules.empty', { server: server.name })}</p>
          ) : (
            <ul>
              {rows.map(({ key, item: s, state }) => (
                <ScheduleRow key={key} server={server} schedule={s} state={state} current={list.data?.current} phone={false} onEdit={() => setEditing(s)} onChanged={changed} />
              ))}
            </ul>
          )}
        </div>
      </Card>
      <Card as="section" aria-labelledby="runs-title" className="pb-2">
        <CardTitle id="runs-title">{t('schedules.runs')}</CardTitle>
        <CardHint>{t('schedules.runsHint')}</CardHint>
        <RecentRuns runs={runs.data?.runs} />
      </Card>
      <ScheduleDialog server={server} editing={editing} onClose={() => setEditing(undefined)} onSaved={changed} />
    </>
  )
}

const runKind: Record<ScheduleKind, MessageKey> = {
  restart: 'schedules.kind.restart',
  backup: 'schedules.kind.backup',
  command: 'schedules.kind.command',
  announcement: 'schedules.kind.announcement',
}

/** What one run did, from what the agent recorded: "312 MB · checked · no downtime". */
export function runText(r: ScheduleRun): string {
  const dot = t('common.dot')
  switch (r.result) {
    case 'skipped':
    case 'missed':
      return reasonText(r.reason, r.result)
    case 'failed':
      return r.detail ? t('schedules.failedDetail', { detail: r.detail }) : reasonText(r.reason, r.result)
    case 'running':
      return t('schedules.result.running')
    case 'succeeded':
      break
    default: {
      const unreachable: never = r.result
      return unreachable
    }
  }
  switch (r.kind) {
    case 'restart': {
      const took = r.startedAt && r.finishedAt ? formatDuration((new Date(r.finishedAt).getTime() - new Date(r.startedAt).getTime()) / 1000) : undefined
      const parts = [took ? t('schedules.took', { time: took }) : t('schedules.done')]
      if (r.players === 0) parts.push(t('schedules.nobodyOn'))
      else if (r.players) parts.push(t('schedules.warned', { count: r.players }))
      return parts.join(dot)
    }
    case 'backup':
      if (!r.backup) return t('schedules.done')
      return [formatBytes(r.backup.sizeBytes), r.backup.verified ? t('schedules.checked') : t('schedules.unchecked'), r.backup.downtimeMs > 0 ? t('schedules.offline', { time: formatDuration(r.backup.downtimeMs / 1000) }) : t('schedules.noDowntime')].join(dot)
    case 'command':
    case 'announcement':
      if (r.detail) return r.detail
      return r.players !== undefined ? t('schedules.sentTo', { count: r.players }) : t('schedules.done')
    default: {
      const unreachable: never = r.kind
      return unreachable
    }
  }
}

function RecentRuns({ runs }: { runs: ScheduleRun[] | undefined }) {
  if (!runs) return <ListSkeleton className="mt-2" rowClassName="flex h-[39px] items-center border-b border-border last:border-b-0" lines={1} />
  if (runs.length === 0) return <p className="mt-2 py-3 text-[13px] text-muted-foreground">{t('schedules.noRuns')}</p>
  return (
    <table className="mt-2 w-full text-[13px]">
      <tbody>
        {runs.map((r, i) => (
          <tr key={`${r.scheduleId}-${r.due}-${i}`} className="border-b border-border last:border-b-0">
            <td className="w-[100px] py-2.5 pr-4 font-semibold whitespace-nowrap">{runDay(r.startedAt ?? r.due)}</td>
            <td className="w-[72px] py-2.5 pr-4 font-medium">{t(runKind[r.kind])}</td>
            <td className={cn('py-2.5 text-muted-foreground', r.result === 'failed' && 'text-destructive-foreground')}>{sentence(runText(r))}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/** The phone's Schedules page, under Settings. */
export function SchedulesPhonePage({ server }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const list = useSchedules(server.id)
  const [editing, setEditing] = useState<Schedule | 'new'>()
  const items = list.data?.schedules
  const rows = useListPresence(items, (s) => s.id)
  return (
    <>
      <PhoneBackHeader to={{ name: 'server', slug: server.slug, tab: 'settings' }} label={t('tab.settings')} title={t('schedules.title')} />
      <div className="flex flex-col gap-2 pb-28">
        <SectionLabel className="px-4">{t('schedules.phoneLabel', { server: server.name })}</SectionLabel>
        <div className="rounded-3xl border border-border bg-white px-4">
          {list.error && !items ? (
            <p className="py-4 text-[15px] text-destructive-foreground">{list.error.message}</p>
          ) : !items ? (
            <RowsSkeleton phone />
          ) : rows.length === 0 ? (
            <p className="animate-fade py-4 text-[15px] text-muted-foreground">{t('schedules.empty', { server: server.name })}</p>
          ) : (
            <ul>
              {rows.map(({ key, item: s, state }) => (
                <ScheduleRow key={key} server={server} schedule={s} state={state} current={list.data?.current} phone onEdit={() => setEditing(s)} onChanged={() => void list.refresh()} />
              ))}
            </ul>
          )}
        </div>
        <p className="px-1 text-[13px] text-muted-foreground">{t('schedules.zone', { zone: zoneLabel() })}</p>
      </div>
      <div className="fixed inset-x-4 bottom-[calc(76px+env(safe-area-inset-bottom))] z-20">
        <Button size="touch" className="w-full" onClick={() => setEditing('new')} disabledReason={newReason(ws.stale, items)}>
          <PlusIcon />
          {t('schedules.new')}
        </Button>
      </div>
      <ScheduleDialog server={server} editing={editing} onClose={() => setEditing(undefined)} onSaved={() => void list.refresh()} />
    </>
  )
}

/** Phone Settings' Schedules row: how many, the next run, and the way in. */
export function SchedulesPhoneRow({ server }: { server: ServerStatus }) {
  const list = useSchedules(server.id)
  const items = list.data?.schedules
  const on = items?.filter((s) => s.enabled) ?? []
  const next = on
    .map((s) => s.nextRun)
    .filter((x): x is string => !!x)
    .sort((a, b) => new Date(a).getTime() - new Date(b).getTime())[0]
  return (
    <a {...linkProps({ name: 'server', slug: server.slug, tab: 'settings', sub: 'schedules' })} className="flex min-h-14 items-center gap-3 py-3 outline-none focus-visible:underline">
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-semibold">{t('schedules.title')}</span>
        {!items ? (
          <span className="mt-0.5 block leading-[18px]">
            <InlineSkeleton className="w-40" />
          </span>
        ) : (
          <span className="mt-0.5 block text-[13px] leading-[18px] text-muted-foreground">
            {items.length === 0 ? t('schedules.rowNone') : next ? t('schedules.rowNext', { count: on.length, when: upcoming(next) }) : t('schedules.rowCount', { count: items.length })}
          </span>
        )}
      </span>
      <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
    </a>
  )
}

// --- the dialog ---

type FormKind = 'restart' | 'backup' | 'command'

interface Form {
  kind: FormKind
  /** "daily", "weekly:fri", "weekly:mon,thu" or "interval:6". */
  often: string
  at: string
  warn: number[]
  /** Warnings the dialog has no box for, such as 30 seconds; kept as they are. */
  otherWarn: number[]
  message: string
  skipIfPlaying: boolean
  onlyIfPlayed: boolean
  command: string
}

const warnBoxes = [600, 300, 60]
const everyHours = [1, 2, 3, 4, 6, 8, 12]

function newForm(server: ServerStatus): Form {
  return { kind: 'restart', often: 'daily', at: '05:00', warn: warnBoxes, otherWarn: [], message: t('schedules.defaultMessage', { server: server.name }), skipIfPlaying: false, onlyIfPlayed: true, command: '' }
}

/** The dialog's form for a schedule, or undefined when the dialog can't show it (a cron timing, an announcement). */
function formOf(s: Schedule): Form | undefined {
  if (s.kind === 'announcement') return undefined
  let often: string
  switch (s.timing.kind) {
    case 'daily':
      often = 'daily'
      break
    case 'weekly':
      often = `weekly:${weekdays.filter((d) => s.timing.days?.includes(d)).join(',')}`
      break
    case 'interval':
      often = `interval:${s.timing.everyHours ?? 6}`
      break
    case 'once':
    case 'cron':
      return undefined
    default: {
      const unreachable: never = s.timing.kind
      return unreachable
    }
  }
  const warn = s.payload.warnSeconds ?? []
  return {
    kind: s.kind,
    often,
    at: s.timing.at ?? '00:00',
    warn: warnBoxes.filter((w) => warn.includes(w)),
    otherWarn: warn.filter((w) => !warnBoxes.includes(w)),
    message: s.payload.message ?? '',
    skipIfPlaying: !!s.payload.skipIfPlaying,
    onlyIfPlayed: !!s.payload.onlyIfPlayed,
    command: s.payload.command ?? '',
  }
}

function timingOf(f: Form, timeZone: string): ScheduleTiming {
  const [kind, arg = ''] = f.often.split(':')
  if (kind === 'interval') return { kind: 'interval', timeZone, at: f.at, everyHours: Number(arg) }
  if (kind === 'weekly') return { kind: 'weekly', timeZone, at: f.at, days: arg.split(',').filter(Boolean) as Schedule['timing']['days'] }
  return { kind: 'daily', timeZone, at: f.at }
}

function payloadOf(f: Form): SchedulePayload {
  switch (f.kind) {
    case 'restart':
      return { warnSeconds: [...f.warn, ...f.otherWarn].sort((a, b) => b - a), message: f.message.trim(), skipIfPlaying: f.skipIfPlaying }
    case 'backup':
      return { skipIfPlaying: f.skipIfPlaying, onlyIfPlayed: f.onlyIfPlayed }
    case 'command':
      return { command: f.command.trim() }
    default: {
      const unreachable: never = f.kind
      return unreachable
    }
  }
}

function oftenChoices(current: string): Choice<string>[] {
  const out: Choice<string>[] = [
    { value: 'daily', label: t('schedules.often.daily') },
    ...weekdays.map((d) => ({ value: `weekly:${d}`, label: t('schedules.often.weekly', { day: dayName(d) }) })),
    { value: 'weekly:mon,tue,wed,thu,fri', label: t('schedules.often.weekdays') },
    { value: 'weekly:sat,sun', label: t('schedules.often.weekends') },
    ...everyHours.map((h) => ({ value: `interval:${h}`, label: h === 1 ? t('schedules.often.hourly') : t('schedules.often.interval', { count: h }) })),
  ]
  if (!out.some((o) => o.value === current) && current.startsWith('weekly:')) {
    const days = current.slice('weekly:'.length).split(',')
    out.splice(1, 0, { value: current, label: sentence(t('schedules.often.days', { days: formatList(days.map((d) => dayName(d))) })) })
  }
  return out
}

function ScheduleDialog({ server, editing, onClose, onSaved }: { server: ServerStatus; editing: Schedule | 'new' | undefined; onClose: () => void; onSaved: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [form, setForm] = useState<Form>(() => newForm(server))
  const [preview, setPreview] = useState<SchedulePreview>()
  const [busy, setBusy] = useState(false)
  const [removing, setRemoving] = useState(false)
  const open = editing !== undefined
  const existing = editing !== undefined && editing !== 'new' ? editing : undefined
  const timeZone = viewerTimeZone()

  useEffect(() => {
    if (editing === undefined) return
    setForm((editing !== 'new' && formOf(editing)) || newForm(server))
    setPreview(undefined)
    // Only a newly opened dialog starts over.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing])

  const body = { kind: form.kind as ScheduleKind, timing: timingOf(form, timeZone), payload: payloadOf(form) }
  const key = JSON.stringify(body)
  useEffect(() => {
    if (!open) return
    let cancelled = false
    const id = window.setTimeout(async () => {
      try {
        const p = await post<SchedulePreview>(serverApi(server.id, '/schedules/preview'), JSON.parse(key))
        if (!cancelled) setPreview(p)
      } catch (e) {
        if (!cancelled) setPreview({ valid: false, nextRuns: [], error: { error: errorText(e), code: e instanceof ApiError ? e.code : 'internal' } })
      }
    }, 300)
    return () => {
      cancelled = true
      window.clearTimeout(id)
    }
  }, [open, key, server.id])

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }))
  async function save() {
    setBusy(true)
    try {
      if (existing) await post(serverApi(server.id, `/schedules/${existing.id}`), body)
      else await post(serverApi(server.id, '/schedules'), { ...body, enabled: true })
      toastManager.add({ title: t('schedules.saved'), type: 'success' })
      onSaved()
      onClose()
    } catch (e) {
      toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  async function remove() {
    if (!existing) return
    setRemoving(true)
    try {
      await del(serverApi(server.id, `/schedules/${existing.id}`))
      toastManager.add({ title: t('schedules.deleted'), type: 'success' })
      onSaved()
      onClose()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setRemoving(false)
    }
  }

  const next = preview?.valid ? preview.nextRuns[0] : undefined
  const problem = preview && !preview.valid ? preview.error : undefined
  const toggleWarn = (w: number, on: boolean) => set('warn', on ? [...form.warn, w].sort((a, b) => b - a) : form.warn.filter((x) => x !== w))
  const noWarnings = form.warn.length + form.otherWarn.length === 0
  const cantSave = ws.stale ? t('reason.noAgent') : !form.at ? t('schedules.pickTime') : problem?.error
  const label = 'text-[13px] font-medium'
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[560px]">
        <div className="px-6 pt-6 pb-1 max-sm:px-5">
          <DialogTitle className="text-lg leading-6 font-bold">{existing ? t('schedules.editTitle') : t('schedules.newTitle', { server: server.name })}</DialogTitle>
        </div>
        <DialogPanel className="flex flex-col gap-4 pt-3 max-sm:px-5">
          <div>
            <div className={label}>{t('schedules.what')}</div>
            <CardGroup value={form.kind} onChange={(k) => set('kind', k)} label={t('schedules.what')} className="mt-1.5 flex flex-wrap gap-2 sm:grid sm:grid-cols-3">
              {(['restart', 'backup', 'command'] as const).map((k) => (
                <ChoiceCard key={k} value={k} radio="start" className="flex-auto items-center gap-2 px-2.5 py-2 text-[13px] font-medium max-sm:px-2 max-sm:whitespace-nowrap">
                  {t(k === 'restart' ? 'schedules.what.restart' : k === 'backup' ? 'schedules.what.backup' : 'schedules.what.command')}
                </ChoiceCard>
              ))}
            </CardGroup>
          </div>
          <div className="flex gap-2.5">
            <div className="min-w-0 flex-1">
              <div className={label}>{t('schedules.howOften')}</div>
              <ChoiceSelect value={form.often} onChange={(v) => set('often', v)} options={oftenChoices(form.often)} label={t('schedules.howOften')} className="mt-1.5 w-full max-sm:border max-sm:border-input" />
            </div>
            <div className="flex shrink-0 flex-col">
              <label htmlFor="schedule-at" className={label}>
                {form.often.startsWith('interval:') ? t('schedules.from') : t('schedules.at')}
              </label>
              {/* Left to its own width, a time field fits its locale's format, 12- or 24-hour. */}
              <Input id="schedule-at" type="time" value={form.at} onChange={(e) => set('at', e.target.value)} className="mt-1.5 w-auto sm:min-w-40" required />
            </div>
          </div>
          {form.kind === 'restart' && (
            <>
              <div>
                <div className={label}>{t('schedules.warnTitle')}</div>
                <div className="mt-2 flex flex-wrap gap-x-5 gap-y-2">
                  {warnBoxes.map((w) => (
                    <label key={w} className="flex items-center gap-2 text-[13px]">
                      <Checkbox checked={form.warn.includes(w)} onCheckedChange={(c) => toggleWarn(w, c === true)} />
                      {t('schedules.warnBox', { count: w / 60 })}
                    </label>
                  ))}
                </div>
                <Input value={form.message} onChange={(e) => set('message', e.target.value)} maxLength={200} disabled={noWarnings} title={noWarnings ? t('schedules.messageOff') : undefined} className="mt-2.5" aria-label={t('schedules.message')} />
                <p className="mt-1 text-xs text-muted-foreground">{t('schedules.messageHint')}</p>
              </div>
              <SwitchRow checked={form.skipIfPlaying} onChange={(c) => set('skipIfPlaying', c)} title={t('schedules.skip')} hint={t('schedules.skipHint')} />
            </>
          )}
          {form.kind === 'backup' && (
            <>
              <SwitchRow checked={form.onlyIfPlayed} onChange={(c) => set('onlyIfPlayed', c)} title={t('schedules.onlyIfPlayed')} hint={t('schedules.onlyIfPlayedHint')} />
              <SwitchRow checked={form.skipIfPlaying} onChange={(c) => set('skipIfPlaying', c)} title={t('schedules.skip')} hint={t('schedules.skipBackupHint')} />
            </>
          )}
          {form.kind === 'command' && (
            <div>
              <label htmlFor="schedule-command" className={label}>
                {t('schedules.command')}
              </label>
              <Input id="schedule-command" value={form.command} onChange={(e) => set('command', e.target.value)} maxLength={256} placeholder={t('schedules.commandPlaceholder')} spellCheck={false} autoComplete="off" className="mt-1.5 font-mono" />
              <p className="mt-1 text-xs text-muted-foreground">{t('schedules.commandHint')}</p>
            </div>
          )}
        </DialogPanel>
        <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
          <p className={cn('min-w-0 flex-1 text-xs', problem ? 'text-destructive-foreground' : 'text-muted-foreground')} aria-live="polite">
            {problem ? (
              <>
                {problem.error}
                {problem.hint && <span className="block text-muted-foreground">{problem.hint}</span>}
              </>
            ) : next ? (
              t('schedules.nextRun', { when: upcomingAt(next) })
            ) : (
              ' '
            )}
          </p>
          <div className="flex gap-2 max-sm:w-full max-sm:flex-col-reverse">
            {existing && (
              <Button variant="ghost" className="text-destructive-foreground" onClick={remove} loading={removing}>
                {!phone && <Trash2Icon />}
                {t('schedules.delete')}
              </Button>
            )}
            <Button variant="ghost" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button onClick={save} loading={busy} disabledReason={cantSave}>
              {t('schedules.save')}
            </Button>
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function SwitchRow({ checked, onChange, title, hint }: { checked: boolean; onChange: (v: boolean) => void; title: string; hint: string }) {
  return (
    <label className="flex items-start gap-3 border-t border-border pt-4">
      <Switch checked={checked} onCheckedChange={onChange} className="mt-0.5" />
      <span>
        <span className="block text-[13px] font-semibold">{title}</span>
        <span className="block text-xs text-muted-foreground">{hint}</span>
      </span>
    </label>
  )
}
