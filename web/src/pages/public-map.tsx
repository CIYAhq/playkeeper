import { useEffect, useMemo, useState } from 'react'
import { ApiError, get } from '@/api/client'
import type { MapPlayer, MapPlayers, MapWorlds, PublicMap } from '@/api/types'
import { BrandMark, Emblem, Pip } from '@/components/app/art'
import { useIsPhone } from '@/components/app/controls'
import { CoordsReadout, MapCoords, MapView, WorldSwitch } from '@/components/app/map-view'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { guessWorlds, sortWorlds } from '@/lib/map'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

const reSlug = /^[a-z0-9][a-z0-9-]{0,39}$/

/** The slug of a shared map page's path, or undefined for any other page. */
export function publicMapSlug(pathname: string): string | undefined {
  const m = /^\/map\/([^/]*)\/?$/.exec(pathname)
  return m ? (m[1] ?? '') : undefined
}

/**
 * The shared map at /map/<server>, for anyone with the link and no
 * sign-in. A map that is turned off, a stopped server and a link that
 * doesn't exist all get the same page, which never names the server.
 */
export function PublicMapPage({ slug }: { slug: string }) {
  const valid = reSlug.test(slug)
  const base = `/api/public/map/${slug}`
  const map = usePoll(() => (valid ? get<PublicMap>(base) : Promise.reject(new ApiError(404, { error: '', code: 'not_found' }))), 30_000, slug)
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
    <div className="flex min-h-dvh flex-col bg-sidebar px-6 pt-4 pb-3 max-sm:px-4 max-sm:pt-[max(env(safe-area-inset-top),16px)] max-sm:pb-[max(env(safe-area-inset-bottom),16px)]">
      {off ? <Unavailable /> : shown ? <SharedMap slug={slug} base={base} info={shown} /> : <Loading />}
      <Footer />
    </div>
  )
}

function Unavailable() {
  const phone = useIsPhone()
  return (
    <main className="flex flex-1 animate-in flex-col items-center justify-center text-center duration-300 fade-in-0">
      <Pip pose="sleep" size={phone ? 88 : 80} />
      <h1 className="mt-4 text-[28px] leading-9 font-bold tracking-[-0.02em] max-sm:text-2xl">{t('publicMap.offTitle')}</h1>
      <p className="mt-2 text-[15px] text-muted-foreground">{t('publicMap.offLead')}</p>
    </main>
  )
}

function Loading() {
  return (
    <main className="flex flex-1 flex-col" aria-busy="true" aria-label={t('common.loading')}>
      <div className="flex items-center gap-3">
        <Skeleton className="size-9 rounded-[22%]" />
        <div className="flex flex-col gap-1.5">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3 w-48" />
        </div>
      </div>
      <Skeleton className="mt-3 min-h-[320px] flex-1 rounded-2xl" />
    </main>
  )
}

function Footer() {
  return (
    <footer className="mt-3 flex items-center justify-between gap-4 text-xs text-muted-foreground max-sm:mt-6 max-sm:flex-col max-sm:gap-1 max-sm:text-center">
      <span className="inline-flex items-center gap-2 text-[13px] text-foreground/80">
        <BrandMark size={16} />
        {t('publicMap.madeWith')}
      </span>
      <p className="max-sm:text-[11px] max-sm:leading-4">{t('footer.notOfficial')}</p>
    </footer>
  )
}

/** The server's own icon when it has one, otherwise the Playkeeper emblem. */
function PublicEmblem({ base, name }: { base: string; name: string }) {
  const [failed, setFailed] = useState(false)
  if (failed) return <Emblem size={36} name={name} />
  return (
    <span className="inline-flex size-9 shrink-0 overflow-hidden rounded-[22%] border border-black/10">
      <img src={`${base}/icon`} width={36} height={36} alt={t('server.emblem', { server: name })} className="pixelated size-full" onError={() => setFailed(true)} />
    </span>
  )
}

function SharedMap({ slug, base, info }: { slug: string; base: string; info: PublicMap }) {
  const phone = useIsPhone()
  const worlds = usePoll(() => get<MapWorlds>(`${base}/worlds`), 60_000, slug)
  const players = usePoll<MapPlayers | undefined>(() => (info.players ? get<MapPlayers>(`${base}/players`) : Promise.resolve(undefined)), 3000, `${slug}:${info.players}`)
  const [coords] = useState(() => new MapCoords())
  const [picked, setPicked] = useState<string>()
  const guess = useMemo(() => guessWorlds(worlds.data?.worlds ?? []), [worlds.data])
  const sorted = useMemo(() => sortWorlds(worlds.data?.worlds ?? [], guess.levelName), [worlds.data, guess])
  const world = sorted.find((w) => w.name === picked) ?? sorted[0]
  const list = info.players ? (players.data?.players ?? []) : []
  const tileURL = useMemo(() => (w: string, zoom: number, x: number, z: number) => `${base}/tiles/${encodeURIComponent(w)}/${zoom}/${x}_${z}.png`, [base])
  const faceURL = useMemo(() => (p: MapPlayer) => `${base}/faces/${encodeURIComponent(p.name)}`, [base])
  const subtitle = info.players ? t('publicMap.playing', { count: list.length }) : t('publicMap.java')
  const caption = info.players ? t('publicMap.shown') : phone ? t('publicMap.hiddenShort') : t('publicMap.hidden')
  const toggle = sorted.length > 1 && world && <WorldSwitch worlds={sorted} value={world.name} onChange={setPicked} serverName={info.name} serverType={guess.serverType} levelName={guess.levelName} large={phone} />

  return (
    <main className="flex flex-1 animate-in flex-col duration-300 fade-in-0">
      <header className="flex items-center gap-3">
        <PublicEmblem base={base} name={info.name} />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[17px] leading-6 font-bold tracking-[-0.01em]">{info.name}</h1>
          <p className="truncate text-[13px] leading-[18px] text-muted-foreground">{subtitle}</p>
        </div>
        {!phone && toggle}
        <CoordsReadout coords={coords} className={cn('text-[13px] text-muted-foreground', !phone && 'ml-2')} />
      </header>
      {phone && toggle && <div className="mt-3">{toggle}</div>}
      {world && worlds.data ? (
        <MapView world={world} tileSize={worlds.data.tileSize} tileURL={tileURL} players={list} faceURL={faceURL} coords={coords} zoomButtons caption={caption} className="mt-3 min-h-[320px] flex-1" />
      ) : (
        <Skeleton className="mt-3 min-h-[320px] flex-1 rounded-2xl" />
      )}
    </main>
  )
}
