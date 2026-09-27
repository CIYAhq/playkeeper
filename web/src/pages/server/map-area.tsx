import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronRightIcon, PlayIcon, RefreshCwIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { MapArea, MapAreaId, MapAreaOption, ServerStatus } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, Marker, Notice, Progress, Spinner } from '@/components/app/bits'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { addonKind } from '@/lib/addons'
import { formatBytes, formatPercent } from '@/lib/format'
import { whyNot } from '@/lib/phase'
import { usePoll, type Poll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { longTime, pausedText, shortTime, stepText } from './world-pregen'

/** Whether the map's area is being filled in: starting, running or paused. */
export function filling(a: MapArea | undefined): boolean {
  const s = a?.fill.state
  return s === 'starting' || s === 'running' || s === 'paused'
}

/** Follows the map's area: every few seconds while it is filled in, slower otherwise. */
export function useMapArea(s: ServerStatus): Poll<MapArea> {
  const [every, setEvery] = useState(30_000)
  const poll = usePoll(() => get<MapArea>(serverApi(s.id, '/map/area')), every, s.id)
  const state = poll.data?.fill.state
  const want = state === 'starting' || state === 'running' ? 3_000 : state === 'paused' ? 10_000 : 30_000
  if (want !== every) setEvery(want)
  return poll
}

/** "Explored only", "2,500 blocks" or "World border". */
export function areaName(a: Pick<MapArea, 'area' | 'radius'>): string {
  switch (a.area) {
    case 'explored':
      return t('mapArea.explored')
    case 'border':
      return t('mapArea.borderShort')
    default:
      return t('pregen.blocks', { radius: a.radius ?? 0 })
  }
}

/** Why an option can't be chosen, which its line also says. */
function optionReason(o: MapAreaOption): string | undefined {
  if (o.done) return t('mapArea.onMap')
  if (o.pastBorder) return t('mapArea.pastBorder')
  if (!o.fits) return t('pregen.noRoom')
  return undefined
}

function optionHint(o: MapAreaOption, a: MapArea): string {
  if (filling(a) && a.area === o.id) return t('mapArea.fillingPct', { percent: formatPercent(Math.floor(a.fill.percent)) })
  const cost = optionReason(o) ?? [t('pregen.about', { time: shortTime(o.seconds) }), formatBytes(o.diskBytes)].join(t('common.dot'))
  return o.id === 'border' ? [t('pregen.blocks', { radius: o.radius }), cost].join(t('common.dot')) : cost
}

/** A choice's name, and on the right (below it on a phone) what it takes. */
function Choice({ title, hint, marker }: { title: string; hint: string; marker?: ReactNode }) {
  return (
    <span className="flex items-baseline justify-between gap-x-4 max-sm:flex-col max-sm:items-start max-sm:gap-0.5">
      <span className="flex items-center gap-2 text-sm font-semibold max-sm:text-[15px]">
        {title}
        {marker}
      </span>
      <span key={hint} className="animate-fade text-xs text-muted-foreground tabular-nums max-sm:text-[13px]">
        {hint}
      </span>
    </span>
  )
}

/**
 * Choosing how much of the world the map shows: the explored land, a size
 * around spawn or up to the world border, each with what filling it in
 * takes. Explored only stops a fill under way; what was generated stays.
 */
export function MapAreaDialog({ open, onOpenChange, server, area }: { open: boolean; onOpenChange: (open: boolean) => void; server: ServerStatus; area: Poll<MapArea> }) {
  const phone = useIsPhone()
  const ws = useWorkspace()
  const place = useServerMachine(server)
  const data = area.data
  const [choice, setChoice] = useState<MapAreaId>('explored')
  const [pause, setPause] = useState(true)
  const [busy, setBusy] = useState(false)
  const seeded = useRef(false)
  const refresh = area.refresh

  useEffect(() => {
    if (open) void refresh()
  }, [open, refresh])
  useEffect(() => {
    if (!open) seeded.current = false
    else if (data && !seeded.current) {
      seeded.current = true
      setChoice(data.area)
      setPause(data.fill.pauseForPlayers)
    }
  }, [open, data])

  const underway = filling(data)
  const unchanged = !data || choice === data.area
  const stopping = choice === 'explored' && underway
  const starting = !unchanged && choice !== 'explored'
  const why = whyNot(server, 'pregen', place.offline) ?? (unchanged ? t('mapArea.unchanged') : undefined)
  const note = stopping ? t('mapArea.stopNote') : starting ? (underway ? t('mapArea.replaceNote') : t('mapArea.load', { server: server.name })) : undefined

  async function confirm() {
    setBusy(true)
    try {
      await post(serverApi(server.id, '/map/area'), { area: choice, pauseForPlayers: pause })
      onOpenChange(false)
      await Promise.all([area.refresh(), ws.refresh()])
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  let body: ReactNode
  if (data) {
    const locked = data.fill.state === 'finished'
    body = (
      <>
        {data.fill.error && !underway && (
          <Notice tone="error" title={t('pregen.failed')} className="mb-3">
            {data.fill.error}
          </Notice>
        )}
        <CardGroup value={choice} onChange={setChoice} label={t('mapArea.title')} className="flex flex-col gap-2">
          <ChoiceCard value="explored" disabled={locked} reason={t('mapArea.stays')} radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-2.5 max-sm:min-h-14">
            <Choice title={t('mapArea.explored')} hint={locked ? t('mapArea.stays') : t('mapArea.exploredHint')} />
          </ChoiceCard>
          {data.options.map((o) => {
            const reason = optionReason(o)
            return (
              <ChoiceCard key={o.id} value={o.id} disabled={!!reason} reason={reason} radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-2.5 max-sm:min-h-14">
                <Choice title={o.id === 'border' ? t('mapArea.border') : t('pregen.blocks', { radius: o.radius })} hint={optionHint(o, data)} marker={o.id === 'medium' && data.area === 'explored' && !reason && <Marker>{t('common.recommended')}</Marker>} />
              </ChoiceCard>
            )
          })}
        </CardGroup>
        {starting && (
          <div className="animate-enter">
            {phone ? (
              <label className="mt-3 flex min-h-12 cursor-pointer items-center gap-3">
                <span className="min-w-0 flex-1 text-[15px]">{t('pregen.pauseForPlayersShort')}</span>
                <Switch checked={pause} onCheckedChange={setPause} />
              </label>
            ) : (
              <label className="mt-4 flex cursor-pointer items-center gap-3 self-start text-[13px] font-semibold">
                <Switch checked={pause} onCheckedChange={setPause} />
                {t('pregen.pauseForPlayers')}
              </label>
            )}
            {!data.fill.installed && (
              <p className={cn('text-muted-foreground', phone ? 'mt-1 text-[13px]' : 'mt-3 text-xs')}>
                {rich(addonKind(server.type) === 'mod' ? 'pregen.installsMod' : 'pregen.installsPlugin', {
                  link: (chunk) => (
                    <a href={t('pregen.learnMoreUrl')} target="_blank" rel="noreferrer" className="font-medium text-primary hover:underline" aria-label={t('common.external', { label: chunk })}>
                      {chunk}
                    </a>
                  ),
                })}
              </p>
            )}
          </div>
        )}
      </>
    )
  } else if (area.error) {
    body = (
      <Notice
        tone="error"
        title={t('map.loadFailed')}
        action={
          <Button variant="outline" size="sm" onClick={() => void refresh()}>
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        }
      >
        {errorText(area.error)}
      </Notice>
    )
  } else {
    body = <ListSkeleton rows={5} lines={1} rowClassName="flex h-12 items-center gap-3 rounded-2xl border border-border px-4" className="flex flex-col gap-2" />
  }

  const action = (
    <Button size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} onClick={confirm} loading={busy} disabledReason={why}>
      {!stopping && <PlayIcon />}
      {stopping ? t('mapArea.stop') : t('pregen.start')}
    </Button>
  )
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={phone}>
        <DialogHeader className="gap-1.5 max-sm:px-5 max-sm:pt-3">
          <DialogTitle className="text-lg leading-6 font-bold">{t('mapArea.title')}</DialogTitle>
          <DialogDescription className="text-[13px] max-sm:mt-2 max-sm:text-[15px]">{t('mapArea.lead')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="max-sm:px-5">{body}</DialogPanel>
        {phone ? (
          <div className="px-5 pt-4">
            {note && (
              <p key={note} className="mb-3 animate-fade text-[13px] text-muted-foreground">
                {note}
              </p>
            )}
            {action}
          </div>
        ) : (
          <DialogFooter variant="bare" className="mx-6 items-center border-t border-border px-0 pt-4">
            {note && (
              <p key={note} className="mr-auto animate-fade text-xs text-muted-foreground">
                {note}
              </p>
            )}
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            {action}
          </DialogFooter>
        )}
      </DialogPopup>
    </Dialog>
  )
}

/** The phone's Map settings row that opens the map's area, with the area it has. */
export function MapAreaRow({ area, onOpen }: { area: MapArea | undefined; onOpen: () => void }) {
  return (
    <button type="button" onClick={onOpen} className="-mx-1 flex min-h-11 items-center gap-2 rounded-lg px-1 text-left text-base transition-colors active:bg-accent/60">
      <span className="min-w-0 flex-1">{t('mapArea.row')}</span>
      {area ? <span className="animate-fade text-muted-foreground">{areaName(area)}</span> : <Skeleton className="h-4 w-20" />}
      <ChevronRightIcon className="size-4 text-muted-foreground" aria-hidden="true" />
    </button>
  )
}

/** The map's area being filled in: how far, how much is done, and why it waits. */
export function FillCard({ server, area, className }: { server: ServerStatus; area: MapArea; className?: string }) {
  const pg = area.fill
  const paused = pg.state === 'paused'
  const to = area.area === 'border' ? t('mapArea.border') : t('mapArea.to', { radius: area.radius ?? 0 })
  const status = paused ? pausedText(pg, server.name) : pg.etaSeconds >= 0 ? t('pregen.left', { time: longTime(pg.etaSeconds) }) : undefined
  return (
    <Card className={cn('animate-enter gap-0 rounded-2xl p-4', className)} aria-live="polite">
      <CardTitle className="text-sm">{t('mapArea.filling')}</CardTitle>
      <p className="mt-0.5 text-xs text-muted-foreground">{to}</p>
      {pg.state === 'starting' ? (
        <p className="mt-3 flex items-center gap-2 text-[13px] font-medium" role="status">
          <Spinner />
          {stepText(pg, server.name)}
        </p>
      ) : (
        <>
          <div className="mt-2.5 flex items-center gap-3">
            <span className="text-[34px] leading-none font-bold tracking-[-0.02em] tabular-nums">{formatPercent(Math.floor(pg.percent))}</span>
            <span className="min-w-0 text-[13px] leading-[18px]">
              <span className="block font-semibold tabular-nums">{t('pregen.chunks', { done: pg.chunks, total: pg.total })}</span>
              {status && (
                <span key={status} className="block animate-fade text-muted-foreground">
                  {status}
                </span>
              )}
            </span>
          </div>
          <Progress value={pg.percent} tone={paused ? 'muted' : 'info'} className="mt-3" label={t('mapArea.filling')} />
        </>
      )}
      {pg.pauseForPlayers && !paused && <p className="mt-3 text-xs text-muted-foreground">{t('pregen.pausesForPlayers')}</p>}
    </Card>
  )
}
