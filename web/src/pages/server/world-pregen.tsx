import { useState } from 'react'
import { MapIcon, PauseIcon, PlayIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { Pregen, PregenPreset, PregenPresetId, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, Marker, Notice, SectionLabel, Spinner } from '@/components/app/bits'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { CardsSkeleton, ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Radio } from '@/components/ui/radio-group'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatBytes, formatPercent } from '@/lib/format'
import { opLabel, whyNot } from '@/lib/phase'
import { usePoll, type Poll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { PhoneActionBar, WorldSubHeader } from './world-sub'

const presetNames: Record<PregenPresetId, MessageKey> = { small: 'pregen.small', medium: 'pregen.medium', large: 'pregen.large', huge: 'pregen.huge' }
const actingKeys: Record<'pause' | 'continue' | 'cancel', MessageKey> = { pause: 'pregen.pausing', continue: 'pregen.resuming', cancel: 'pregen.cancelling' }
const modServers = new Set(['fabric', 'quilt', 'neoforge'])

/** A length of time, rounded the way people say it: minutes, half hours under ten hours, hours, then days. */
export function roughTime(seconds: number): { unit: 'min' | 'h' | 'days'; count: number } {
  const s = Math.max(0, seconds)
  const minutes = Math.max(1, Math.round(s / 60))
  if (minutes < 60) return { unit: 'min', count: minutes }
  const hours = s / 3600
  if (hours < 9.75) return { unit: 'h', count: Math.max(1, Math.round(hours * 2) / 2) }
  if (hours < 47.5) return { unit: 'h', count: Math.round(hours) }
  return { unit: 'days', count: Math.round(s / 86400) }
}

/** "5 min", "1.5 h" or "3 days". */
export function shortTime(seconds: number): string {
  const r = roughTime(seconds)
  switch (r.unit) {
    case 'min':
      return t('pregen.min', { count: r.count })
    case 'h':
      return t('pregen.h', { count: r.count })
    case 'days':
      return t('time.duration.days', { count: r.count })
    default: {
      const unreachable: never = r.unit
      return unreachable
    }
  }
}

/** "14 minutes", "1.5 hours" or "3 days". */
export function longTime(seconds: number): string {
  const r = roughTime(seconds)
  switch (r.unit) {
    case 'min':
      return t('time.duration.minutes', { count: r.count })
    case 'h':
      return t('time.duration.hours', { count: r.count })
    case 'days':
      return t('time.duration.days', { count: r.count })
    default: {
      const unreachable: never = r.unit
      return unreachable
    }
  }
}

/** Whole percent, so 99.6 still reads 99% until the task is done. */
function percent(pg: Pregen): string {
  return formatPercent(Math.floor(pg.percent))
}

function stepText(pg: Pregen, server: string): string {
  switch (pg.step) {
    case 'installing':
      return t('pregen.installing')
    case 'restarting':
      return t('pregen.restarting', { server })
    case 'starting_server':
      return t('pregen.startingServer', { server })
    case 'starting_task':
    case undefined:
      return t('pregen.startingTask')
    default: {
      const unreachable: never = pg.step
      return unreachable
    }
  }
}

/** Why a paused task waits, such as "Paused while mara_k plays". */
export function pausedText(pg: Pregen, server: string): string {
  switch (pg.pausedBy) {
    case 'players':
      return pg.pausedFor ? t('pregen.pausedFor', { name: pg.pausedFor }) : t('pregen.pausedPlayers')
    case 'server':
      return t('pregen.pausedServer', { server })
    case 'user':
    case undefined:
      return t('pregen.pausedUser')
    default: {
      const unreachable: never = pg.pausedBy
      return unreachable
    }
  }
}

/** The second line of the World card's "Pre-generate the map" row. */
export function pregenLine(pg: Pregen | undefined, server: string): string {
  if (!pg) return t('world.pregenHint')
  switch (pg.state) {
    case 'idle':
      return t('world.pregenHint')
    case 'starting':
      return stepText(pg, server)
    case 'running':
      return [t('pregen.rowActive'), percent(pg), pg.etaSeconds >= 0 ? t('pregen.aboutLeft', { time: shortTime(pg.etaSeconds) }) : undefined].filter(Boolean).join(t('common.dot'))
    case 'paused':
      return t('pregen.rowPaused', { percent: percent(pg) })
    case 'finished':
      return t('pregen.rowReady', { radius: pg.radius ?? 0 })
    default: {
      const unreachable: never = pg.state
      return unreachable
    }
  }
}

/**
 * The preset chosen first: after a finished task the next bigger one, even
 * when it doesn't fit so the page says why; otherwise Medium, or the biggest
 * smaller one that fits on the disk.
 */
export function firstPreset(presets: PregenPreset[], after?: number): PregenPresetId {
  if (after !== undefined) {
    const bigger = presets.filter((p) => p.radius > after)
    const next = bigger.find((p) => p.fits) ?? bigger[0]
    if (next) return next.id
  }
  const medium = presets.find((p) => p.id === 'medium')
  if (!medium || medium.fits) return 'medium'
  return [...presets].reverse().find((p) => p.fits && p.radius < medium.radius)?.id ?? 'medium'
}

/** Follows the server's pre-generation: every few seconds while it runs, slower otherwise. */
export function usePregen(s: ServerStatus): Poll<Pregen> {
  const [every, setEvery] = useState(30_000)
  const poll = usePoll(() => get<Pregen>(serverApi(s.id, '/pregen')), every, s.id)
  const state = poll.data?.state
  const want = state === 'starting' || state === 'running' ? 3_000 : state === 'paused' ? 10_000 : 30_000
  if (want !== every) setEvery(want)
  return poll
}

export function PregenPage({ server: s }: { server: ServerStatus }) {
  const poll = usePregen(s)
  const pg = poll.data
  const [further, setFurther] = useState(false)
  const refresh = poll.refresh
  let view: 'loading' | 'error' | 'choose' | 'progress' | 'finished' = 'loading'
  if (pg) view = pg.state === 'idle' || (pg.state === 'finished' && further) ? 'choose' : pg.state === 'finished' ? 'finished' : 'progress'
  else if (poll.error) view = 'error'
  return (
    <>
      <WorldSubHeader server={s} title={t('pregen.phoneTitle')} />
      <div key={view} className="flex animate-fade flex-col">
        {view === 'loading' && <PregenSkeleton />}
        {view === 'error' && poll.error && <PregenError message={poll.error.code === 'unsupported_server' ? t('pregen.unsupported') : errorText(poll.error)} unsupported={poll.error.code === 'unsupported_server'} onRetry={refresh} />}
        {view === 'choose' && pg && (
          <Chooser
            server={s}
            pregen={pg}
            onStarted={async () => {
              await refresh()
              setFurther(false)
            }}
          />
        )}
        {view === 'progress' && pg && <Running server={s} pregen={pg} onChanged={refresh} />}
        {view === 'finished' && pg && <Finished pregen={pg} onFurther={pg.presets.some((p) => p.radius > (pg.radius ?? 0)) ? () => setFurther(true) : undefined} />}
      </div>
    </>
  )
}

function PregenSkeleton() {
  const phone = useIsPhone()
  if (phone) {
    return (
      <div className="flex flex-col gap-3 pt-1">
        <p className="px-1 text-[15px] leading-5 text-muted-foreground">{t('pregen.hint')}</p>
        <section>
          <SectionLabel className="px-4">{t('pregen.howFar')}</SectionLabel>
          <ListSkeleton rows={4} rowClassName="flex min-h-[60px] items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0" className="mt-2 overflow-hidden rounded-3xl border border-border bg-white" trailing={<Skeleton className="size-5 shrink-0 rounded-full" />} />
        </section>
        <Skeleton className="h-14 w-full rounded-3xl" />
      </div>
    )
  }
  return (
    <Card>
      <CardTitle>{t('world.pregen')}</CardTitle>
      <CardHint>{t('pregen.hint')}</CardHint>
      <CardsSkeleton count={4} className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-4" card="h-[92px]" />
      <Skeleton className="mt-7 h-5 w-64" />
    </Card>
  )
}

function PregenError({ message, unsupported, onRetry }: { message: string; unsupported: boolean; onRetry: () => Promise<void> }) {
  return (
    <Card>
      <CardTitle>{t('world.pregen')}</CardTitle>
      <CardHint>{t('pregen.hint')}</CardHint>
      <Notice
        className="mt-4"
        tone={unsupported ? 'warning' : 'error'}
        title={message}
        action={
          !unsupported && (
            <Button variant="outline" size="sm" onClick={() => void onRetry()}>
              {t('common.tryAgain')}
            </Button>
          )
        }
      />
    </Card>
  )
}

function Chooser({ server: s, pregen: pg, onStarted }: { server: ServerStatus; pregen: Pregen; onStarted: () => Promise<void> }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [preset, setPreset] = useState<PregenPresetId>(() => firstPreset(pg.presets, pg.state === 'finished' ? pg.radius : undefined))
  const [pause, setPause] = useState(pg.pauseForPlayers)
  const [busy, setBusy] = useState(false)
  const chosen = pg.presets.find((p) => p.id === preset)
  const otherJob = s.operation && s.operation.kind !== 'pregen-start' ? s.operation : undefined
  const blocked = whyNot({ ...s, operation: otherJob }, 'pregen', ws.stale) ?? (chosen?.fits ? undefined : t('pregen.noRoom'))

  async function start() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/pregen/start'), { preset, pauseForPlayers: pause })
      await onStarted()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const estimate = (p: PregenPreset) => (p.fits ? [t('pregen.about', { time: shortTime(p.seconds) }), formatBytes(p.diskBytes)].join(t('common.dot')) : t('pregen.noRoom'))
  const name = (p: PregenPreset) => (
    <span className="flex items-center gap-2">
      <span className={phone ? 'text-base' : 'text-[15px] font-semibold'}>{t(presetNames[p.id])}</span>
      {p.id === 'medium' && <Marker>{t('common.recommended')}</Marker>}
    </span>
  )
  const chunky = pg.installed ? null : (
    <p className={cn('text-muted-foreground', phone ? 'px-1 text-[13px]' : 'text-xs')}>
      {rich(modServers.has(s.type) ? 'pregen.installsMod' : 'pregen.installsPlugin', {
        link: (chunk) =>
          phone ? null : (
            <a href={t('pregen.learnMoreUrl')} target="_blank" rel="noreferrer" className="font-medium text-primary hover:underline" aria-label={t('common.external', { label: chunk })}>
              {chunk}
            </a>
          ),
      })}
    </p>
  )
  const failed = pg.error && <Notice tone="error" title={t('pregen.failed')}>{pg.error}</Notice>
  const startButton = (
    <Button size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} onClick={start} loading={busy} disabledReason={blocked}>
      <PlayIcon />
      {t('pregen.start')}
    </Button>
  )
  const note = otherJob ? (
    <span className="inline-flex items-center gap-1.5">
      <Spinner />
      {opLabel(otherJob, s.name)}
    </span>
  ) : pg.diskFreeBytes !== undefined ? (
    t('pregen.diskFree', { machine: ws.machineName, free: formatBytes(pg.diskFreeBytes) })
  ) : null

  if (phone) {
    return (
      <div className="flex flex-col gap-3 pt-1">
        <p className="px-1 text-[15px] leading-5 text-muted-foreground">{t('pregen.hint')}</p>
        {failed}
        <section aria-labelledby="pregen-far">
          <SectionLabel className="px-4">
            <span id="pregen-far">{t('pregen.howFar')}</span>
          </SectionLabel>
          <CardGroup value={preset} onChange={setPreset} label={t('pregen.howFar')} className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {pg.presets.map((p) => (
              <label key={p.id} className="flex min-h-[60px] cursor-pointer items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0 active:bg-accent/60 has-[[data-disabled]]:cursor-default has-[[data-disabled]]:opacity-60" title={p.fits ? undefined : t('pregen.noRoom')}>
                <span className="min-w-0 flex-1">
                  {name(p)}
                  <span className="block text-[13px] text-muted-foreground">{[t('pregen.blocks', { radius: p.radius }), estimate(p)].join(t('common.dot'))}</span>
                </span>
                <Radio value={p.id} disabled={!p.fits} className="shrink-0" />
              </label>
            ))}
          </CardGroup>
        </section>
        <label className="flex min-h-14 cursor-pointer items-center gap-3 rounded-3xl border border-border bg-white px-4">
          <span className="min-w-0 flex-1 text-base">{t('pregen.pauseForPlayers')}</span>
          <Switch checked={pause} onCheckedChange={setPause} />
        </label>
        {chunky}
        {otherJob && <p className="px-1 text-[13px] text-muted-foreground">{note}</p>}
        <PhoneActionBar>{startButton}</PhoneActionBar>
      </div>
    )
  }

  return (
    <Card>
      <CardTitle>{t('world.pregen')}</CardTitle>
      <CardHint>{t('pregen.hint')}</CardHint>
      {failed && <div className="mt-4">{failed}</div>}
      <p className="mt-5 text-[13px] font-semibold">{t('pregen.howFar')}</p>
      <CardGroup value={preset} onChange={setPreset} label={t('pregen.howFar')} className="mt-2.5 grid grid-cols-2 gap-3 lg:grid-cols-4">
        {pg.presets.map((p) => (
          <ChoiceCard key={p.id} value={p.id} disabled={!p.fits} reason={t('pregen.noRoom')} className="items-start px-4 py-3.5">
            {name(p)}
            <span className="mt-2 block text-[13px] font-semibold tabular-nums">{t('pregen.blocks', { radius: p.radius })}</span>
            <span className="mt-0.5 block text-xs text-muted-foreground">{estimate(p)}</span>
          </ChoiceCard>
        ))}
      </CardGroup>
      <label className="mt-7 flex cursor-pointer items-center gap-3 self-start text-[13px] font-semibold">
        <Switch checked={pause} onCheckedChange={setPause} />
        {t('pregen.pauseForPlayers')}
      </label>
      {chunky && <div className="mt-7">{chunky}</div>}
      <div className="mt-5 flex items-center gap-4 border-t border-border pt-4">
        <p className="min-w-0 flex-1 text-xs text-muted-foreground">{note}</p>
        {startButton}
      </div>
    </Card>
  )
}

function Bar({ value, tone, label, className }: { value: number; tone: 'info' | 'muted'; label: string; className?: string }) {
  const pct = Math.max(0, Math.min(100, value))
  return (
    <div className={cn('h-2 w-full overflow-hidden rounded-full bg-foreground/8', className)} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.floor(pct)} aria-label={label}>
      <div className={cn('h-full rounded-full transition-[width,background-color] duration-(--motion-slow) ease-standard', tone === 'info' ? 'bg-info' : 'bg-muted-foreground/35')} style={{ width: `${pct}%` }} />
    </div>
  )
}

function Running({ server: s, pregen: pg, onChanged }: { server: ServerStatus; pregen: Pregen; onChanged: () => Promise<void> }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [acting, setActing] = useState<'pause' | 'continue' | 'cancel'>()
  const starting = pg.state === 'starting'
  const paused = pg.state === 'paused'

  async function act(action: 'pause' | 'continue' | 'cancel') {
    setActing(action)
    try {
      await post(serverApi(s.id, `/pregen/${action}`))
      await onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setActing(undefined)
    }
  }

  const presetLine = pg.preset && pg.radius ? t('pregen.presetLine', { preset: t(presetNames[pg.preset]), radius: pg.radius }) : undefined
  const status = paused ? pausedText(pg, s.name) : pg.etaSeconds >= 0 ? t('pregen.left', { time: longTime(pg.etaSeconds) }) : undefined
  const resume = paused && pg.pausedBy !== 'server'
  const size = phone ? 'touch' : 'default'
  const blocked = ws.stale ? t('reason.noAgent') : acting ? t('reason.busy', { what: t(actingKeys[acting]) }) : undefined
  const actions = !starting && (
    <>
      <Button variant="outline" size={size} className={phone ? 'flex-1' : undefined} onClick={() => void act(resume ? 'continue' : 'pause')} loading={acting === 'continue' || acting === 'pause'} disabledReason={blocked}>
        {resume ? <PlayIcon /> : <PauseIcon />}
        {resume ? t('pregen.resume') : t('pregen.pause')}
      </Button>
      <Button variant="ghost" size={size} className={phone ? 'flex-1' : undefined} onClick={() => void act('cancel')} loading={acting === 'cancel'} disabledReason={blocked}>
        {t('common.cancel')}
      </Button>
    </>
  )
  const reading = starting ? (
    <p className="mt-4 flex items-center gap-2 text-[13px] font-medium" role="status">
      <Spinner />
      {stepText(pg, s.name)}
    </p>
  ) : (
    <>
      <div className="mt-4 flex items-center gap-4">
        <span className="text-[44px] leading-none font-bold tracking-[-0.03em] tabular-nums">{percent(pg)}</span>
        <span className="min-w-0">
          <span className="block text-[13px] font-semibold tabular-nums">{t('pregen.chunks', { done: pg.chunks, total: pg.total })}</span>
          {status && (
            <span key={status} className="block animate-fade text-[13px] text-muted-foreground">
              {status}
            </span>
          )}
        </span>
      </div>
      <Bar value={pg.percent} tone={paused ? 'muted' : 'info'} label={t('world.pregen')} className="mt-4" />
    </>
  )
  const pauses = !paused && pg.pauseForPlayers && t('pregen.pausesForPlayers')

  if (phone) {
    return (
      <div className="flex flex-col gap-3 pt-1">
        <Card className="p-4">
          {presetLine && <p className="text-[13px] font-semibold text-muted-foreground">{presetLine}</p>}
          {reading}
        </Card>
        {pauses && <p className="px-1 text-[13px] text-muted-foreground">{pauses}</p>}
        {actions && <PhoneActionBar>{actions}</PhoneActionBar>}
      </div>
    )
  }
  return (
    <Card>
      <div className="flex items-start gap-4">
        <div className="min-w-0 flex-1">
          <CardTitle>{t('world.pregen')}</CardTitle>
          {presetLine && <CardHint>{presetLine}</CardHint>}
        </div>
        {actions && <div className="flex gap-2">{actions}</div>}
      </div>
      {reading}
      {pauses && <p className="mt-4 text-xs text-muted-foreground">{pauses}</p>}
    </Card>
  )
}

function Finished({ pregen: pg, onFurther }: { pregen: Pregen; onFurther?: () => void }) {
  const phone = useIsPhone()
  const chunks = t('unit.chunks', { count: pg.chunks || pg.total })
  const line = [pg.elapsedSeconds ? t('pregen.done', { chunks, time: longTime(pg.elapsedSeconds) }) : chunks, pg.diskBytes !== undefined ? formatBytes(pg.diskBytes) : undefined].filter(Boolean).join(t('common.dot'))
  const further = onFurther && (
    <Button variant="outline" size={phone ? 'touch' : 'default'} className={phone ? 'mt-4 w-full' : undefined} onClick={onFurther}>
      <MapIcon />
      {t('pregen.goFurther')}
    </Button>
  )
  return (
    <Card className={phone ? 'mt-1 p-4' : 'py-6'}>
      <div className="flex items-center gap-5 max-sm:gap-4">
        <Pip pose="cheer" size={phone ? 56 : 64} />
        <div className="min-w-0 flex-1">
          <h2 className="text-lg leading-6 font-bold tracking-[-0.01em]">{t('pregen.ready', { radius: pg.radius ?? 0 })}</h2>
          <p className="mt-0.5 text-[13px] text-muted-foreground">{line}</p>
        </div>
        {!phone && further}
      </div>
      {phone && further}
    </Card>
  )
}
