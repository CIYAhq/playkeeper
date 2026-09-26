import { useState, type ReactNode } from 'react'
import { SunIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { Operation, ServerStatus, SessionsResponse, SleepStatus, SleepView } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { PlayerFace } from '@/components/app/bits'
import { ChoiceSelect, SettingRow, useIsPhone, type Choice } from '@/components/app/controls'
import { InlineSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatClock, formatDuration, formatMB } from '@/lib/format'
import { whyNot } from '@/lib/phase'
import { linkPath } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { viewerTimeZone } from '@/lib/when'
import { serverAction } from '.'

// Wave 7: sleep when nobody's playing. The Memory group's rows, the asleep
// Overview card and the bits Home and the Overview cards show while asleep.

const idleOptions = [5, 10, 15, 30, 60, 120, 240]

/** "15 minutes" or "2 hours". */
export function idleText(minutes: number): string {
  return minutes >= 60 && minutes % 60 === 0 ? t('time.duration.hours', { count: minutes / 60 }) : t('time.duration.minutes', { count: minutes })
}

/** The sleep setting, with how often it slept today in the viewer's day. */
export function useSleep(serverId: string) {
  return usePoll(() => get<SleepView>(serverApi(serverId, `/sleep?tz=${encodeURIComponent(viewerTimeZone())}`)), 60_000, serverId)
}

async function wake(s: ServerStatus): Promise<void> {
  await serverAction(s, 'start')
}

/** Memory group: "Sleep when nobody's playing", then when it falls asleep and what friends see. */
export function SleepRows({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const view = useSleep(s.id)
  const [pending, setPending] = useState<{ enabled: boolean; idleMinutes: number }>()
  const current = view.data ?? s.sleep
  const enabled = pending?.enabled ?? current?.enabled ?? false
  const idle = pending?.idleMinutes ?? (current?.idleMinutes || view.data?.defaultIdleMinutes || 15)
  const min = view.data?.minIdleMinutes ?? 5
  const max = view.data?.maxIdleMinutes ?? 24 * 60
  const choices: Choice<string>[] = [...new Set([...idleOptions, idle])]
    .filter((m) => m >= min && m <= max)
    .sort((a, b) => a - b)
    .map((m) => ({ value: String(m), label: t('sleep.afterOption', { time: idleText(m) }) }))
  const locked = ws.stale ? t('reason.noAgent') : !current ? t('common.loading') : pending ? t('reason.saving') : undefined

  async function save(next: { enabled: boolean; idleMinutes: number }) {
    setPending(next)
    try {
      const r = await post<{ sleep: SleepStatus; operation?: Operation }>(serverApi(s.id, '/sleep'), next)
      const title = next.enabled ? t('sleep.onToast', { server: s.name, time: idleText(next.idleMinutes) }) : r.operation ? t('sleep.offWaking', { server: s.name }) : t('sleep.offToast', { server: s.name })
      toastManager.add({ title, type: 'success' })
      await Promise.all([view.refresh(), ws.refresh()])
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setPending(undefined)
    }
  }

  const toggle = <Switch checked={enabled} onCheckedChange={(c) => void save({ enabled: c, idleMinutes: idle })} disabled={!!locked} title={locked} aria-label={t('settings.sleep')} />
  const after = <ChoiceSelect value={String(idle)} onChange={(v) => void save({ enabled, idleMinutes: Number(v) })} options={choices} label={t('sleep.after')} disabledReason={locked} className={phone ? undefined : 'mt-1.5 w-[240px]'} />
  // The rows below the switch open and close with it, so it never has a line under it of its own.
  const reveal = (children: ReactNode) => (
    <div className={cn('grid transition-[grid-template-rows,opacity] duration-(--motion-standard) ease-standard', enabled ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0')} inert={!enabled}>
      <div className="min-h-0 overflow-hidden">{children}</div>
    </div>
  )
  if (phone) {
    return (
      <>
        <SettingRow label={t('settings.sleep')} hint={t('settings.sleepHint')} control={toggle} className="border-b-0" />
        {reveal(
          <>
            <SettingRow label={<span className="whitespace-nowrap">{t('sleep.after')}</span>} control={after} className="border-t" />
            <SettingRow label={t('sleep.friendsSee')} hint={t('sleep.friendsSeeBody')} control={null} />
          </>,
        )}
      </>
    )
  }
  return (
    <>
      <SettingRow label={t('settings.sleep')} hint={t('settings.sleepHint')} control={toggle} className="border-b-0" />
      {reveal(
        <div className="grid grid-cols-[240px_minmax(0,1fr)] gap-6 pb-3.5">
          <div>
            <div className="text-[13px] font-semibold">{t('sleep.after')}</div>
            {after}
          </div>
          <div>
            <div className="text-[13px] font-semibold">{t('sleep.friendsSee')}</div>
            <p className="mt-1 text-[13px] leading-[18px] text-muted-foreground">{t('sleep.friendsSeeBody')}</p>
          </div>
        </div>,
      )}
    </>
  )
}

/** Overview's card while the server sleeps: why, and Wake up now. */
export function AsleepCard({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const idle = idleText(s.sleep?.idleMinutes || 15)
  const run = async () => {
    setBusy(true)
    await wake(s)
    setBusy(false)
  }
  const button = can(ws.me, 'servers.run') && (
    <Button size={phone ? 'touch' : 'default'} className={cn(phone && 'mt-4 w-full')} onClick={run} loading={busy} disabledReason={whyNot(s, 'start', ws.stale)}>
      <SunIcon />
      {t('sleep.wake')}
    </Button>
  )
  return (
    <section aria-labelledby="asleep-title" className="rounded-3xl border border-border bg-card p-5 shadow-card animate-fade max-sm:p-4">
      <div className="flex items-center gap-5 max-sm:items-start max-sm:gap-3">
        <Pip pose="sleep" size={phone ? 64 : 76} className="shrink-0" />
        <div className="min-w-0 flex-1">
          <h2 id="asleep-title" className="text-lg leading-6 font-bold">
            {t('sleep.title', { server: s.name })}
          </h2>
          <p className="mt-0.5 text-[13px] leading-[18px] text-muted-foreground max-sm:text-[15px] max-sm:leading-5">{s.sleep?.listening === false ? t('sleep.whyDeaf', { time: idle }) : t('sleep.why', { time: idle })}</p>
        </div>
        {!phone && (
          <div className="flex shrink-0 flex-col items-end gap-2">
            {button}
            {can(ws.me, 'servers.manage') && (
              <a {...linkPath(`/servers/${s.slug}/settings#memory`)} className="text-xs font-medium text-primary hover:underline">
                {t('sleep.settings')}
              </a>
            )}
          </div>
        )}
      </div>
      {phone && button}
    </section>
  )
}

/** The Join address card's last line while asleep. */
export function ListeningLine({ server: s }: { server: ServerStatus }) {
  const listening = s.sleep?.listening ?? false
  return (
    <>
      <span className={cn('size-2 rounded-full border-[1.5px] transition-colors duration-(--motion-standard) ease-standard', listening ? 'border-muted-foreground/60' : 'border-warning-foreground')} aria-hidden="true" />
      {listening ? t('sleep.listening') : t('sleep.notListening')}
    </>
  )
}

/** Who left last before the server fell asleep, and who played today. */
export function LastOneOut({ server: s }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const sessions = usePoll(() => get<SessionsResponse>(serverApi(s.id, '/players/sessions?range=24h')), 60_000, s.id)
  const list = sessions.data?.sessions
  if (!list) {
    return sessions.loading ? (
      <p className="mt-2 text-[13px]">
        <InlineSkeleton className="w-52" />
      </p>
    ) : null
  }
  const ended = list.filter((x) => x.end).sort((a, b) => new Date(b.end ?? 0).getTime() - new Date(a.end ?? 0).getTime())
  const last = ended[0]
  const midnight = new Date()
  midnight.setHours(0, 0, 0, 0)
  const today = [...new Set(list.filter((x) => new Date(x.end ?? x.start) >= midnight).map((x) => x.player))]
  const line = !last?.end
    ? t('sleep.noneLately')
    : phone
      ? t('sleep.lastOutShort', { player: last.player, time: formatClock(last.end) })
      : t('sleep.lastOut', { player: last.player, time: formatClock(last.end), duration: formatDuration(last.durationSeconds) })
  return (
    <>
      <p className={cn('mt-2 text-[13px] text-muted-foreground', phone && 'text-[13px]')}>{line}</p>
      {!phone && today.length > 0 && (
        <p className="mt-auto flex items-center gap-2 pt-4 text-xs text-muted-foreground">
          <span className="flex gap-1">
            {today.slice(0, 3).map((n) => (
              <PlayerFace key={n} name={n} size={22} />
            ))}
          </span>
          {t('sleep.playedToday')}
        </p>
      )}
    </>
  )
}

/** "Asleep 3 times today, for 6 h in total", from the agent's sleep periods. */
export function SleepToday({ server: s }: { server: ServerStatus }) {
  const view = useSleep(s.id)
  const today = view.data?.today
  if (!today) return view.loading ? <InlineSkeleton className="w-48" /> : null
  return <>{today.count === 0 ? t('sleep.allDay') : t('sleep.today', { count: today.count, time: formatDuration(Math.max(60, today.seconds)) })}</>
}

/** Home's card detail for a sleeping server: "Asleep · wakes on join" and Wake up. */
export function AsleepDetail({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  return (
    <>
      <Pip pose="sleep" size={40} />
      <span className="text-[13px] text-muted-foreground">{s.sleep?.listening === false ? t('sleep.cardDeaf') : t('sleep.card')}</span>
      {!s.operation && can(ws.me, 'servers.run') && (
        <Button
          variant="outline"
          size="sm"
          className="relative z-10 ml-auto"
          loading={busy}
          disabledReason={whyNot(s, 'start', ws.stale)}
          onClick={async () => {
            setBusy(true)
            await wake(s)
            setBusy(false)
          }}
        >
          <SunIcon />
          {t('sleep.wakeShort')}
        </Button>
      )}
    </>
  )
}

/** "Survival gave back 4 GB", for the machine's memory line. */
export function gaveBackText(servers: ServerStatus[] | undefined, sleepingMemoryMB: number | undefined): string | undefined {
  if (!sleepingMemoryMB) return undefined
  const asleep = (servers ?? []).filter((s) => s.phase === 'asleep')
  const memory = formatMB(sleepingMemoryMB)
  return asleep.length === 1 && asleep[0] ? t('sleep.gaveBack', { server: asleep[0].name, memory }) : t('sleep.gaveBackMany', { memory })
}
