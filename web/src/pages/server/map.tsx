import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { CheckIcon, ChevronLeftIcon, CopyIcon, EllipsisIcon, MapIcon, PlayIcon, RefreshCwIcon, RotateCwIcon } from 'lucide-react'
import { get, post, type ApiError } from '@/api/client'
import type { MapInfo, MapPlayer, MapPlayers, MapWorld, MapWorlds, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip, type PipPose } from '@/components/app/art'
import { Card, CardTitle, copyText, PlayerFace, Progress, useNow } from '@/components/app/bits'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { CoordsReadout, MapCoords, MapView, WorldSwitch, type MapFocus } from '@/components/app/map-view'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuItem, MenuPopup, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatBytes, formatClock, formatDate, formatPercent, formatSpan, relativeTime } from '@/lib/format'
import { coord, hasMap, sortWorlds, worldLabel } from '@/lib/map'
import { phaseTone } from '@/lib/phase'
import { linkProps, navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { serverAction } from '.'

function tilePath(id: string) {
  return (world: string, zoom: number, x: number, z: number) => serverApi(id, `/map/tiles/${encodeURIComponent(world)}/${zoom}/${x}_${z}.png`)
}

function faceURL(p: MapPlayer): string {
  return `/api/players/${encodeURIComponent(p.name)}/head${p.uuid ? `?uuid=${encodeURIComponent(p.uuid)}` : ''}`
}

/**
 * The Map tab: turning the map on, waiting for its restart, watching it
 * draw, the live map with who's playing and sharing, and what to do when
 * the server is stopped or the map doesn't answer.
 */
export function MapPage({ server }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const ws = useWorkspace()
  const supported = hasMap(server)
  const info = usePoll(() => get<MapInfo>(serverApi(server.id, '/map')), 5000, server.id)
  const [menuOpen, setMenuOpen] = useState(false)
  const [offOpen, setOffOpen] = useState(false)
  const [enabling, setEnabling] = useState(false)
  const op = ws.stale ? undefined : server.operation?.kind
  const mapOp = op === 'map_enable' || op === 'map_disable'
  const refreshInfo = info.refresh
  const state = info.data?.state

  useEffect(() => {
    if (!supported || state === 'unsupported') navigate({ name: 'server', slug: server.slug, tab: 'overview' }, true)
  }, [supported, state, server.slug])

  // A job starting or ending changes what the map can do; ask again right away.
  const seen = useRef({ op, phase: server.phase })
  useEffect(() => {
    if (seen.current.op === op && seen.current.phase === server.phase) return
    seen.current = { op, phase: server.phase }
    void refreshInfo()
  }, [op, server.phase, refreshInfo])

  async function refresh() {
    await Promise.all([info.refresh(), ws.refresh()])
  }

  async function enable() {
    setEnabling(true)
    try {
      await post(serverApi(server.id, '/map/enable'))
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setEnabling(false)
    }
  }

  if (!supported) return null
  const data = info.data
  let body: ReactNode
  if (!data) {
    body = info.error ? <LoadFailed error={info.error} onRetry={info.refresh} /> : <MapSkeleton phone={phone} />
  } else if (enabling || mapOp) {
    body = <SetupState server={server} info={data} busy onEnable={enable} />
  } else {
    switch (data.state) {
      case 'unsupported':
        body = null
        break
      case 'not_installed':
        body = <SetupState server={server} info={data} busy={false} onEnable={enable} />
        break
      case 'needs_restart':
        body = <RestartState server={server} info={data} onChange={refresh} />
        break
      case 'server_stopped':
        body = <StoppedState server={server} />
        break
      case 'not_answering':
        body = <DownState server={server} info={data} onRetry={info.refresh} />
        break
      case 'drawing':
      case 'ready':
        body = <LiveMap server={server} info={data} onChange={info.refresh} onTurnOff={() => setOffOpen(true)} menuOpen={menuOpen} onMenuOpenChange={setMenuOpen} />
        break
      default: {
        const unreachable: never = data.state
        body = unreachable
      }
    }
  }
  const live = !!data && !enabling && !mapOp && data.state === 'ready'
  return (
    <>
      {phone && <PhoneMapHeader onMenu={live ? () => setMenuOpen(true) : undefined} />}
      {body}
      {data?.enabled && <TurnOffDialog open={offOpen} onOpenChange={setOffOpen} server={server} info={data} onDone={refresh} />}
    </>
  )
}

/** The phone's Map header: back to More, the title in the middle, and the map's settings. */
function PhoneMapHeader({ onMenu }: { onMenu?: () => void }) {
  return (
    <header className="grid grid-cols-[1fr_auto_1fr] items-center pt-2 pb-2">
      <a {...linkProps({ name: 'more' })} className="-ml-2 inline-flex min-h-11 items-center gap-0.5 justify-self-start rounded-lg px-1 text-[15px] font-medium text-success-strong">
        <ChevronLeftIcon className="size-5" aria-hidden="true" />
        {t('nav.more')}
      </a>
      <h1 className="text-[17px] font-semibold">{t('tab.map')}</h1>
      <div className="justify-self-end">
        {onMenu && (
          <Button variant="ghost" size="icon-xl" className="-mr-2" aria-label={t('map.settings')} aria-haspopup="dialog" onClick={onMenu}>
            <EllipsisIcon />
          </Button>
        )}
      </div>
    </header>
  )
}

/** Pip, a title and a line, and what to do: the map tab's states without a map. */
function StateScreen({ pose, title, lead, facts, note, children }: { pose: PipPose; title: string; lead: ReactNode; facts?: string[]; note?: ReactNode; children?: ReactNode }) {
  const phone = useIsPhone()
  return (
    <section className="flex flex-1 animate-in flex-col items-center py-12 text-center duration-300 fade-in-0 max-sm:justify-center max-sm:py-8">
      <Pip pose={pose} size={phone ? 80 : 88} />
      <h2 className="mt-4 text-[22px] leading-7 font-bold tracking-[-0.015em] max-sm:text-xl">{title}</h2>
      <p className="mt-1.5 text-[15px] text-muted-foreground max-sm:text-[15px]">{lead}</p>
      {facts && (
        <ul className="mt-5 flex flex-col gap-1 text-sm">
          {facts.map((f) => (
            <li key={f}>{f}</li>
          ))}
        </ul>
      )}
      {children && <div className="mt-5 flex items-center gap-2 max-sm:mt-4 max-sm:w-full max-sm:flex-col-reverse max-sm:gap-3 max-sm:[&>*]:w-full">{children}</div>}
      {note && <p className="mt-3.5 max-w-[440px] text-xs leading-[18px] text-muted-foreground max-sm:text-[13px]">{note}</p>}
    </section>
  )
}

function SetupState({ server, info, busy, onEnable }: { server: ServerStatus; info: MapInfo; busy: boolean; onEnable: () => void }) {
  const phone = useIsPhone()
  const online = phaseTone(server.phase) === 'online'
  const playing = server.players?.online ?? 0
  const note = !online ? t('map.turnOnStopped', { server: server.name }) : playing > 0 ? t('map.turnOnAsk', { server: server.name }) : t('map.turnOnRestart', { server: server.name })
  return (
    <StateScreen
      pose="search"
      title={t('map.setupTitle')}
      lead={t('map.setupLead')}
      facts={[t('map.setupTime', { minutes: info.estimatedMinutes, server: server.name }), t('map.setupDisk', { megabytes: info.estimatedMegabytes }), t('map.setupShare')]}
      note={note}
    >
      <Button size={phone ? 'touch' : 'default'} onClick={onEnable} loading={busy}>
        <MapIcon />
        {t('map.turnOn')}
      </Button>
    </StateScreen>
  )
}

function RestartState({ server, info, onChange }: { server: ServerStatus; info: MapInfo; onChange: () => Promise<void> }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState<'now' | 'later' | null>(null)
  const playing = server.players?.online ?? 0
  const restarting = server.operation?.kind === 'restart' || phaseTone(server.phase) === 'busy'
  async function now() {
    setBusy('now')
    await serverAction(server, 'restart')
    await onChange()
    setBusy(null)
  }
  async function later() {
    setBusy('later')
    try {
      await post(serverApi(server.id, '/map/restart-later'))
      await onChange()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(null)
    }
  }
  const note = info.restartWhenEmpty ? t('map.restartWaiting', { server: server.name }) : playing > 0 ? t('map.restartPlaying', { count: playing }) : undefined
  return (
    <StateScreen pose="hardhat" title={phone ? t('map.restartTitlePhone') : t('map.restartTitle')} lead={t('map.restartLead', { server: server.name })} note={note}>
      {!info.restartWhenEmpty && (
        <Button variant="outline" size={phone ? 'touch' : 'default'} onClick={later} loading={busy === 'later'} disabled={busy !== null || restarting}>
          {t('map.restartLater')}
        </Button>
      )}
      <Button size={phone ? 'touch' : 'default'} onClick={now} loading={busy === 'now' || restarting} disabled={busy !== null}>
        <RotateCwIcon />
        {t('map.restartNow')}
      </Button>
    </StateScreen>
  )
}

function StoppedState({ server }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const starting = phaseTone(server.phase) === 'busy' || server.operation !== undefined
  async function start() {
    setBusy(true)
    await serverAction(server, 'start')
    setBusy(false)
  }
  return (
    <StateScreen pose="sleep" title={t('map.stoppedTitle')} lead={t('map.stoppedLead')}>
      <Button size={phone ? 'touch' : 'default'} onClick={start} loading={busy || starting}>
        <PlayIcon />
        {t('map.startServer', { server: server.name })}
      </Button>
    </StateScreen>
  )
}

function lastDrawn(iso: string | undefined): string | undefined {
  if (!iso) return undefined
  const d = new Date(iso)
  const today = new Date()
  const same = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate()
  return same ? t('map.lastDrawnToday', { time: formatClock(iso) }) : t('map.lastDrawnDay', { date: formatDate(iso), time: formatClock(iso) })
}

function DownState({ server, info, onRetry }: { server: ServerStatus; info: MapInfo; onRetry: () => Promise<void> }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState<'retry' | 'restart' | null>(null)
  async function retry() {
    setBusy('retry')
    // Long enough to see that something happened when the answer is quick.
    await Promise.all([onRetry(), new Promise((r) => window.setTimeout(r, 500))])
    setBusy(null)
  }
  async function restart() {
    setBusy('restart')
    await serverAction(server, 'restart')
    setBusy(null)
  }
  return (
    <StateScreen pose="hurt" title={t('map.downTitle')} lead={t('map.downLead')} note={lastDrawn(info.lastDrawn)}>
      <Button variant="outline" size={phone ? 'touch' : 'default'} onClick={restart} loading={busy === 'restart'} disabled={busy !== null || server.operation !== undefined}>
        <RotateCwIcon />
        {t('map.restartServer', { server: server.name })}
      </Button>
      <Button size={phone ? 'touch' : 'default'} onClick={retry} loading={busy === 'retry'} disabled={busy !== null}>
        <RefreshCwIcon />
        {t('common.tryAgain')}
      </Button>
    </StateScreen>
  )
}

function LoadFailed({ error, onRetry }: { error: ApiError; onRetry: () => Promise<void> }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  return (
    <StateScreen pose="hurt" title={t('map.loadFailed')} lead={errorText(error)}>
      <Button
        size={phone ? 'touch' : 'default'}
        loading={busy}
        onClick={async () => {
          setBusy(true)
          await Promise.all([onRetry(), new Promise((r) => window.setTimeout(r, 500))])
          setBusy(false)
        }}
      >
        <RefreshCwIcon />
        {t('common.tryAgain')}
      </Button>
    </StateScreen>
  )
}

function MapSkeleton({ phone }: { phone: boolean }) {
  if (phone) {
    return (
      <div className="flex flex-1 flex-col gap-3 pb-4" aria-busy="true" aria-label={t('common.loading')}>
        <Skeleton className="h-11 w-full rounded-xl" />
        <Skeleton className="min-h-[360px] flex-1 rounded-2xl" />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-3" aria-busy="true" aria-label={t('common.loading')}>
      <div className="flex h-9 items-center gap-3">
        <Skeleton className="h-8 w-56 rounded-[9px]" />
        <Skeleton className="ml-auto h-4 w-44" />
        <Skeleton className="size-8 rounded-lg" />
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)_272px] gap-3.5">
        <Skeleton className="h-[425px] rounded-2xl" />
        <div className="flex flex-col gap-3.5">
          <Skeleton className="h-[140px] rounded-2xl" />
          <Skeleton className="h-[180px] rounded-2xl" />
        </div>
      </div>
    </div>
  )
}

function LiveMap({ server, info, onChange, onTurnOff, menuOpen, onMenuOpenChange }: { server: ServerStatus; info: MapInfo; onChange: () => Promise<void>; onTurnOff: () => void; menuOpen: boolean; onMenuOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  const drawing = info.state === 'drawing'
  const worlds = usePoll(() => get<MapWorlds>(serverApi(server.id, '/map/worlds')), 60_000, `${server.id}:${info.state}`)
  const players = usePoll(() => get<MapPlayers>(serverApi(server.id, '/map/players')), 3000, server.id)
  const [coords] = useState(() => new MapCoords())
  const [picked, setPicked] = useState<string>()
  const [focus, setFocus] = useState<MapFocus>()
  const levelName = server.config?.levelName
  const sorted = useMemo(() => sortWorlds(worlds.data?.worlds ?? [], levelName), [worlds.data, levelName])
  const world = sorted.find((w) => w.name === picked) ?? sorted[0]
  const list = players.data?.players ?? []
  const tileURL = useMemo(() => tilePath(server.id), [server.id])

  function find(p: MapPlayer) {
    if (!sorted.some((w) => w.name === p.world)) return
    setPicked(p.world)
    setFocus((f) => ({ x: p.x, z: p.z, seq: (f?.seq ?? 0) + 1 }))
  }

  const map =
    world && worlds.data ? (
      <MapView world={world} tileSize={worlds.data.tileSize} tileURL={tileURL} players={drawing ? undefined : list} faceURL={faceURL} focus={focus} coords={coords} className={phone ? (drawing ? 'aspect-square w-full' : 'min-h-[360px] flex-1') : 'min-h-[356px]'} />
    ) : worlds.error ? (
      <MapUnavailable error={worlds.error} onRetry={worlds.refresh} className={phone ? 'min-h-[360px] flex-1' : 'min-h-[356px]'} />
    ) : (
      <Skeleton className={cn('rounded-2xl', phone ? (drawing ? 'aspect-square w-full' : 'min-h-[360px] flex-1') : 'min-h-[356px]')} />
    )
  const toggle = sorted.length > 1 && world && <WorldSwitch worlds={sorted} value={world.name} onChange={setPicked} serverName={server.name} serverType={server.type || 'paper'} levelName={levelName} large={phone} />

  if (phone) {
    return (
      <div className="flex flex-1 flex-col gap-3 pb-4">
        {!drawing && toggle}
        {map}
        {drawing ? <DrawingCard info={info} /> : <PhonePlaying players={list} onFind={find} />}
        <Sheet open={menuOpen} onOpenChange={onMenuOpenChange}>
          <SheetPopup side="bottom" className="px-5">
            <div className="pt-3 pb-4">
              <SheetTitle className="text-lg font-bold">{t('map.settings')}</SheetTitle>
            </div>
            <SharingControls server={server} info={info} onChange={onChange} large />
            <div className="my-4 h-px bg-border" aria-hidden="true" />
            <button
              type="button"
              onClick={() => {
                onMenuOpenChange(false)
                onTurnOff()
              }}
              className="-mx-1 mb-1 min-h-11 rounded-lg px-1 text-left text-base text-destructive-foreground"
            >
              {t('map.turnOffMenu')}
            </button>
          </SheetPopup>
        </Sheet>
      </div>
    )
  }
  return (
    <div className="flex animate-in flex-col gap-3 duration-300 fade-in-0">
      <div className="flex min-h-9 flex-wrap items-center gap-x-4 gap-y-2">
        {toggle}
        <div className="ml-auto flex items-center gap-4 text-[13px] text-muted-foreground">
          <CoordsReadout coords={coords} className="text-foreground/80" />
          {drawing ? <span>{t('map.drawingFirst')}</span> : <Drawn at={info.lastDrawn} />}
          <Menu>
            <MenuTrigger render={<Button variant="outline" size="icon" aria-label={t('map.options')} />}>
              <EllipsisIcon />
            </MenuTrigger>
            <MenuPopup align="end" className="min-w-48">
              <MenuItem variant="destructive" onClick={onTurnOff}>
                {t('map.turnOffMenu')}
              </MenuItem>
            </MenuPopup>
          </Menu>
        </div>
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)_272px] items-stretch gap-3.5">
        {map}
        <div className="flex flex-col gap-3.5">
          {drawing && <DrawingCard info={info} />}
          <PlayingCard players={list} world={world} worlds={sorted} levelName={levelName} onFind={find} />
          {!drawing && (
            <Card className="gap-0 rounded-2xl p-4">
              <SharingControls server={server} info={info} onChange={onChange} />
            </Card>
          )}
        </div>
      </div>
    </div>
  )
}

function Drawn({ at }: { at?: string }) {
  const now = useNow(15_000)
  if (!at) return null
  return <span>{t('map.drawn', { time: relativeTime(at, now) })}</span>
}

function MapUnavailable({ error, onRetry, className }: { error: ApiError; onRetry: () => Promise<void>; className?: string }) {
  return (
    <div className={cn('flex flex-col items-center justify-center gap-3 rounded-2xl border border-border bg-[#E9EAE3] p-6 text-center', className)}>
      <p className="text-sm font-semibold">{t('map.loadFailed')}</p>
      <p className="-mt-2 text-[13px] text-muted-foreground">{errorText(error)}</p>
      <Button variant="outline" size="sm" onClick={() => void onRetry()}>
        <RefreshCwIcon />
        {t('common.tryAgain')}
      </Button>
    </div>
  )
}

function where(p: MapPlayer, world: MapWorld | undefined, worlds: MapWorld[], levelName?: string): string {
  const x = coord(p.x)
  const z = coord(p.z)
  if (!world || p.world === world.name) return t('map.at', { x, z })
  const in_ = worlds.find((w) => w.name === p.world)
  return t('map.atIn', { world: in_ ? worldLabel(in_, levelName, 'long') : p.world, x, z })
}

function PlayingCard({ players, world, worlds, levelName, onFind }: { players: MapPlayer[]; world?: MapWorld; worlds: MapWorld[]; levelName?: string; onFind: (p: MapPlayer) => void }) {
  return (
    <Card className="gap-2.5 rounded-2xl p-4">
      <CardTitle className="text-sm">{t('map.playing')}</CardTitle>
      {players.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">{t('map.nobody')}</p>
      ) : (
        <ul className="-mx-1.5 flex max-h-[228px] flex-col gap-0.5 overflow-y-auto">
          {players.map((p) => (
            <li key={p.uuid || p.name}>
              <button type="button" onClick={() => onFind(p)} title={t('map.find', { name: p.name })} className="flex w-full items-center gap-2.5 rounded-lg px-1.5 py-1 text-left outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring">
                <PlayerFace name={p.name} uuid={p.uuid} size={24} />
                <span className="min-w-0">
                  <span className="block truncate text-[13px] leading-[18px] font-semibold">{p.name}</span>
                  <span className="block truncate text-xs leading-4 text-muted-foreground tabular-nums">{where(p, world, worlds, levelName)}</span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

/** The phone's line under the map: faces (tap one to find them) and how many are playing. */
function PhonePlaying({ players, onFind }: { players: MapPlayer[]; onFind: (p: MapPlayer) => void }) {
  if (players.length === 0) return <p className="text-[15px] text-muted-foreground">{t('map.nobody')}</p>
  return (
    <div className="flex items-center gap-2.5">
      <div className="flex">
        {players.slice(0, 5).map((p, i) => (
          <button type="button" key={p.uuid || p.name} onClick={() => onFind(p)} aria-label={t('map.find', { name: p.name })} className={cn('rounded-[7px] ring-2 ring-sidebar', i > 0 && '-ml-1.5')}>
            <PlayerFace name={p.name} uuid={p.uuid} size={28} />
          </button>
        ))}
      </div>
      <span className="text-[15px] text-muted-foreground">{t('status.playing', { count: players.length })}</span>
    </div>
  )
}

function DrawingCard({ info }: { info: MapInfo }) {
  const p = info.progress
  return (
    <Card className="gap-0 rounded-2xl p-4" aria-live="polite">
      <CardTitle className="text-sm">{t('map.drawingTitle')}</CardTitle>
      {p ? (
        <>
          <div className="mt-2.5 flex items-center gap-3">
            <span className="text-[34px] leading-none font-bold tracking-[-0.02em] tabular-nums">{formatPercent(p.percent)}</span>
            <span className="min-w-0 text-[13px] leading-[18px]">
              <span className="block font-semibold tabular-nums">{t('map.areas', { done: p.done, total: p.total })}</span>
              {p.secondsLeft !== undefined && <span className="block text-muted-foreground">{t('map.timeLeft', { time: formatSpan(p.secondsLeft) })}</span>}
            </span>
          </div>
          <Progress value={p.percent} tone="info" className="mt-3" label={t('map.drawingTitle')} />
        </>
      ) : (
        <div className="mt-3 h-1.5 w-full animate-pulse rounded-full bg-info/35" role="progressbar" aria-label={t('map.drawingTitle')} />
      )}
      <p className="mt-3 text-xs text-muted-foreground">{t('map.keepPlaying')}</p>
    </Card>
  )
}

/** Shows a link with a line break allowed after the host. */
function LinkText({ link }: { link: string }) {
  const shown = link.replace(/^https?:\/\//, '')
  const cut = shown.indexOf('/')
  if (cut < 0) return <>{shown}</>
  return (
    <>
      {shown.slice(0, cut + 1)}
      <wbr />
      {shown.slice(cut + 1)}
    </>
  )
}

function CopyLink({ link, large }: { link: string; large?: boolean }) {
  const [done, setDone] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  async function copy() {
    const ok = await copyText(link)
    toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
    if (!ok) return
    setDone(true)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setDone(false), 1800)
  }
  if (large) {
    return (
      <div className="flex items-center gap-2 rounded-xl bg-muted py-1.5 pr-1.5 pl-3">
        <span className="min-w-0 flex-1 text-[15px] leading-5 break-words">
          <LinkText link={link} />
        </span>
        <Button variant="outline" size="icon-xl" onClick={copy} aria-label={t('map.copyLink')}>
          {done ? <CheckIcon /> : <CopyIcon />}
        </Button>
      </div>
    )
  }
  return (
    <div className="flex items-start gap-1.5 rounded-lg bg-muted py-1.5 pr-1 pl-2.5">
      <span className="min-w-0 flex-1 py-0.5 text-xs leading-[18px] break-words">
        <LinkText link={link} />
      </span>
      <Button variant="ghost" size="icon-xs" onClick={copy} aria-label={t('map.copyLink')} className="text-muted-foreground">
        {done ? <CheckIcon /> : <CopyIcon />}
      </Button>
    </div>
  )
}

/**
 * The two sharing switches: the link (with Copy), and whether the shared
 * map shows players. Without a friendly address the link uses the address
 * this dashboard was opened on, and suggests setting one up first.
 */
function SharingControls({ server, info, onChange, large }: { server: ServerStatus; info: MapInfo; onChange: () => Promise<void>; large?: boolean }) {
  const ws = useWorkspace()
  const shareId = useId()
  const playersId = useId()
  const [pending, setPending] = useState<{ public?: boolean; players?: boolean }>({})
  const isPublic = pending.public ?? info.public
  const showPlayers = pending.players ?? info.publicPlayers
  const link = info.link || `${window.location.origin}${info.path}`

  async function change(field: 'public' | 'players', value: boolean) {
    setPending((p) => ({ ...p, [field]: value }))
    try {
      await post(serverApi(server.id, '/map/share'), { [field]: value })
      await onChange()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setPending((p) => {
        const next = { ...p }
        delete next[field]
        return next
      })
    }
  }

  const title = large ? 'text-[17px] leading-6 font-semibold' : 'text-sm font-semibold'
  const hint = large ? 'mt-0.5 text-[13px] leading-[18px] text-muted-foreground' : 'mt-0.5 text-xs leading-4 text-muted-foreground'
  return (
    <div className="flex flex-col">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div id={shareId} className={title}>
            {t('map.share')}
          </div>
          <p className={hint}>{t('map.shareHint')}</p>
        </div>
        <Switch checked={isPublic} onCheckedChange={(v) => void change('public', v)} aria-labelledby={shareId} disabled={pending.public !== undefined} className="mt-0.5" />
      </div>
      {isPublic && (
        <div className="animate-in duration-200 fade-in-0">
          <div className={large ? 'mt-4' : 'mt-3'}>
            <CopyLink link={link} large={large} />
            {!info.link && ws.machine && (
              <p className={cn(hint, 'mt-2')}>
                {rich('map.noAddress', {
                  address: (chunk) => (
                    <a {...linkProps({ name: 'machine', id: ws.machine?.id ?? '' })} className="font-medium text-primary underline underline-offset-2">
                      {chunk}
                    </a>
                  ),
                })}
              </p>
            )}
          </div>
          <div className={cn('h-px bg-border', large ? 'my-4 bg-transparent' : 'my-3')} aria-hidden="true" />
          <div className="flex items-start gap-3">
            <div className="min-w-0 flex-1">
              <div id={playersId} className={title}>
                {t('map.sharePlayers')}
              </div>
              <p className={hint}>{t('map.sharePlayersHint')}</p>
            </div>
            <Switch checked={showPlayers} onCheckedChange={(v) => void change('players', v)} aria-labelledby={playersId} disabled={pending.players !== undefined} className="mt-0.5" />
          </div>
        </div>
      )}
    </div>
  )
}

function TurnOffDialog({ open, onOpenChange, server, info, onDone }: { open: boolean; onOpenChange: (open: boolean) => void; server: ServerStatus; info: MapInfo; onDone: () => Promise<void> }) {
  const phone = useIsPhone()
  const [choice, setChoice] = useState<'keep' | 'delete'>('keep')
  const [busy, setBusy] = useState(false)
  const online = phaseTone(server.phase) === 'online'
  const size = info.bytes > 0 ? formatBytes(info.bytes) : undefined

  useEffect(() => {
    if (open) setChoice('keep')
  }, [open])

  async function confirm() {
    setBusy(true)
    try {
      await post(serverApi(server.id, '/map/disable'), { deleteMap: choice === 'delete' })
      onOpenChange(false)
      await onDone()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={phone}>
        <DialogHeader className="gap-1.5 max-sm:px-5 max-sm:pt-3">
          <DialogTitle className="text-lg leading-6 font-bold">{t('map.offTitle')}</DialogTitle>
          <DialogDescription className="text-[13px] max-sm:mt-2 max-sm:text-[15px]">{online ? t('map.offLead', { server: server.name }) : t('map.offLeadStopped')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="max-sm:px-5">
          <CardGroup value={choice} onChange={setChoice} label={t('map.offTitle')} className="flex flex-col gap-2.5">
            <ChoiceCard value="keep" radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-3">
              <span className="block text-sm font-semibold max-sm:text-[15px]">{t('map.offKeep')}</span>
              <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{size ? t('map.offKeepHint', { size }) : null}</span>
            </ChoiceCard>
            <ChoiceCard value="delete" radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-3">
              <span className="block text-sm font-semibold max-sm:text-[15px]">{t('map.offDelete')}</span>
              <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{size ? t('map.offDeleteHint', { size }) : null}</span>
            </ChoiceCard>
          </CardGroup>
        </DialogPanel>
        {phone ? (
          <div className="px-5 pt-6">
            <Button size="touch" className="w-full" onClick={confirm} loading={busy}>
              {t('map.offConfirm')}
            </Button>
          </div>
        ) : (
          <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4">
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirm} loading={busy}>
              {t('map.offConfirm')}
            </Button>
          </DialogFooter>
        )}
      </DialogPopup>
    </Dialog>
  )
}
