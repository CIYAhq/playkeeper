import { useEffect, useMemo, useState } from 'react'
import { ApiError, get } from '@/api/client'
import type { MapPlayer, MapPlayers, MapWorlds, PublicMap } from '@/api/types'
import { BrandMark, Emblem, Pip } from '@/components/app/art'
import { useIsPhone } from '@/components/app/controls'
import { CoordsReadout, MapCoords, MapView, WorldSwitch } from '@/components/app/map-view'
import { InlineSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { guessWorlds, sortWorlds } from '@/lib/map'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** A shared map's link token: 22 letters and digits, like invite codes. */
const reToken = /^[A-Za-z0-9]{22}$/

/** The link token in a shared map page's path, or undefined for any other page. */
export function publicMapToken(pathname: string): string | undefined {
  const m = /^\/map\/([^/]*)\/?$/.exec(pathname)
  return m ? (m[1] ?? '') : undefined
}

/**
 * The shared map at /map/<link token>, for anyone with the link and no
 * sign-in. A map that is turned off, a stopped server and a link that
 * doesn't exist all get the same page, which never names the server.
 */
export function PublicMapPage({ token }: { token: string }) {
  const valid = reToken.test(token)
  const base = `/api/public/map/${token}`
  const map = usePoll(() => (valid ? get<PublicMap>(base) : Promise.reject(new ApiError(404, { error: '', code: 'not_found' }))), 30_000, token)
  const off = !valid || map.error?.status === 404
  const [last, setLast] = useState<PublicMap>()
  useEffect(() => {
    if (map.data) setLast(map.data)
  }, [map.data])
  const shown = off ? undefined : (map.data ?? last)

  useEffect(() => {
    document.title = shown ? t('publicMap.title', { server: shown.name }) : off ? t('publicMap.offTitle') : t('brand.name')
  }, [shown, off])

  return (
    <div className="flex min-h-dvh flex-col bg-sidebar px-8 pt-4 pb-3.5 max-sm:px-4 max-sm:pt-[max(env(safe-area-inset-top),16px)] max-sm:pb-[max(env(safe-area-inset-bottom),16px)]">
      {off ? <Unavailable /> : shown ? <SharedMap token={token} base={base} info={shown} /> : <Loading />}
      <Footer />
    </div>
  )
}

function Unavailable() {
  return (
    <main className="flex flex-1 animate-fade flex-col items-center justify-center pt-13 text-center max-sm:pt-0">
      <Pip pose="sleep" size={120} />
      <h1 className="mt-4 text-[28px] leading-9 font-bold tracking-[-0.02em] max-sm:mt-3 max-sm:text-2xl max-sm:leading-8">{t('publicMap.offTitle')}</h1>
      <p className="mt-4 text-base text-muted-foreground max-sm:mt-3">{t('publicMap.offLead')}</p>
    </main>
  )
}

function Loading() {
  return (
    <main className="flex flex-1 flex-col">
      <LoadingLabel />
      <div className="flex items-center gap-4 max-sm:gap-2.5">
        <Skeleton className="size-9 rounded-[22%] max-sm:size-8" />
        <div className="flex flex-col gap-1.5">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3 w-48" />
        </div>
      </div>
      <Skeleton className="mt-4 min-h-[320px] flex-1 rounded-2xl max-sm:mt-3" />
    </main>
  )
}

function Footer() {
  return (
    <footer className="mt-4 flex items-center justify-between gap-4 text-xs text-muted-foreground max-sm:mt-9 max-sm:flex-col max-sm:gap-0.5 max-sm:text-center">
      <span className="inline-flex items-center gap-2 text-[13px] text-foreground/80">
        <BrandMark size={18} className="max-sm:size-4" />
        {t('publicMap.madeWith')}
      </span>
      <p className="max-sm:text-[11px] max-sm:leading-[13px]">{t('footer.notOfficial')}</p>
    </footer>
  )
}

/** The server's own icon when it has one, otherwise the Playkeeper emblem. */
function PublicEmblem({ base, name, size }: { base: string; name: string; size: number }) {
  const [failed, setFailed] = useState(false)
  if (failed) return <Emblem size={size} name={name} />
  return (
    <span className="inline-flex shrink-0 overflow-hidden rounded-[22%] border border-black/10" style={{ width: size, height: size }}>
      <img src={`${base}/icon`} width={size} height={size} alt={t('server.emblem', { server: name })} className="pixelated size-full" onError={() => setFailed(true)} />
    </span>
  )
}

function SharedMap({ token, base, info }: { token: string; base: string; info: PublicMap }) {
  const phone = useIsPhone()
  const worlds = usePoll(() => get<MapWorlds>(`${base}/worlds`), 60_000, token)
  const players = usePoll<MapPlayers | undefined>(() => (info.players ? get<MapPlayers>(`${base}/players`) : Promise.resolve(undefined)), 3000, `${token}:${info.players}`)
  const [coords] = useState(() => new MapCoords())
  const [picked, setPicked] = useState<string>()
  const guess = useMemo(() => guessWorlds(worlds.data?.worlds ?? []), [worlds.data])
  const sorted = useMemo(() => sortWorlds(worlds.data?.worlds ?? [], guess.levelName), [worlds.data, guess])
  const world = sorted.find((w) => w.name === picked) ?? sorted[0]
  const list = info.players ? players.data?.players : []
  const tileURL = useMemo(() => (w: string, zoom: number, x: number, z: number) => `${base}/tiles/${encodeURIComponent(w)}/${zoom}/${x}_${z}.png`, [base])
  const faceURL = useMemo(() => (p: MapPlayer) => `${base}/faces/${encodeURIComponent(p.name)}`, [base])
  const subtitle = !info.players ? t('publicMap.java') : list ? t('publicMap.playing', { count: list.length }) : <InlineSkeleton className="w-24" />
  const caption = info.players ? t('publicMap.shown') : phone ? t('publicMap.hiddenShort') : t('publicMap.hidden')
  const toggle = sorted.length > 1 && world && <WorldSwitch worlds={sorted} value={world.name} onChange={setPicked} serverName={info.name} serverType={guess.serverType} levelName={guess.levelName} large={phone} />

  return (
    <main className="flex flex-1 animate-fade flex-col">
      <header className="flex items-center gap-4 max-sm:gap-2.5">
        <PublicEmblem base={base} name={info.name} size={phone ? 32 : 36} />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[17px] leading-5 font-bold tracking-[-0.01em]">{info.name}</h1>
          <p className="truncate text-[13px] leading-4 text-muted-foreground">{subtitle}</p>
        </div>
        {!phone && toggle}
        <CoordsReadout coords={coords} className={cn('text-[13px] text-muted-foreground', !phone && 'ml-2')} />
      </header>
      {phone && toggle && <div className="mt-3">{toggle}</div>}
      {world && worlds.data ? (
        <MapView world={world} tileSize={worlds.data.tileSize} tileURL={tileURL} players={list} faceURL={faceURL} coords={coords} zoomButtons caption={caption} className="mt-4 min-h-[320px] flex-1 max-sm:mt-3" />
      ) : (
        <Skeleton className="mt-4 min-h-[320px] flex-1 rounded-2xl max-sm:mt-3" />
      )}
    </main>
  )
}
