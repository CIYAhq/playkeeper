import { useEffect, useState, type ReactNode } from 'react'
import { ArrowUpRightIcon, CheckIcon, PlayIcon } from 'lucide-react'
import { get } from '@/api/client'
import type { PublicBoard, PublicPage, PublicServer, PublicStream } from '@/api/types'
import ground from '@/assets/pixel-art/ground.svg'
import { BrandMark, Emblem, Pip, TypeLogo } from '@/components/app/art'
import { CopyButton, Dot, PlayerFace, useNow } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { relativeTime } from '@/lib/format'
import { joinSteps, publicStatus, serverPageApi, serverPageFace, serverPageIcon, serverPageTitle, sessionTime, streamEmbed, untilText } from '@/lib/server-page'
import { typeName } from '@/lib/servers'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** Where the footer's call to action leads, tagged so the site can count visits from server pages. */
const siteLink = 'https://playkeeper.io/?ref=server-page'

/**
 * The public page at the machine's address, for anyone who types a server's
 * address into a browser: live status, how to join and the shared map, with
 * Playkeeper's footer. It needs no sign-in and loads none of the dashboard.
 * A page that is off and an address that isn't this machine's get the same
 * page, which names no server.
 */
export function ServerPage() {
  const poll = usePoll(() => get<PublicPage>(serverPageApi), 15_000)
  const [last, setLast] = useState<PublicPage>()
  useEffect(() => {
    if (poll.data) setLast(poll.data)
  }, [poll.data])
  // A dropped connection keeps what was shown; only the page's own 404, or a
  // failure before anything was shown, reads as no page.
  const gone = poll.error?.status === 404 || (!!poll.error && !last) || (poll.data && poll.data.servers.length === 0)
  const shown = gone ? undefined : (poll.data ?? last)

  useEffect(() => {
    document.title = shown ? serverPageTitle(shown.address, shown.servers) : gone ? t('serverPage.goneTitle') : t('brand.name')
  }, [shown, gone])

  return (
    <div className="flex min-h-dvh flex-col bg-sidebar">
      <main className="mx-auto flex w-full max-w-[920px] flex-1 flex-col justify-center px-6 py-14 max-sm:justify-start max-sm:px-4 max-sm:pt-4 max-sm:pb-8">
        {gone ? <Gone /> : shown ? <Servers page={shown} /> : <Loading />}
      </main>
      <BrandBand />
      <footer className="bg-[#0e3b21] px-6 pb-5 text-center text-[11px] text-white/55 max-sm:px-4 max-sm:pb-[max(env(safe-area-inset-bottom),20px)]">{t('footer.notOfficial')}</footer>
    </div>
  )
}

function Servers({ page }: { page: PublicPage }) {
  const [first] = page.servers
  if (page.servers.length === 1 && first) return <ServerCard server={first} heading="h1" />
  return (
    <div className="flex flex-col gap-5 max-sm:gap-4">
      <header className="max-sm:px-1">
        <h1 className="text-[28px] leading-9 font-bold tracking-[-0.02em] break-all max-sm:text-2xl max-sm:leading-8">{page.address}</h1>
        <p className="mt-1 text-[15px] text-muted-foreground">{t('serverPage.servers', { count: page.servers.length })}</p>
      </header>
      {page.servers.map((s) => (
        <ServerCard key={s.slug} server={s} heading="h2" />
      ))}
    </div>
  )
}

function ServerCard({ server: s, heading: Heading }: { server: PublicServer; heading: 'h1' | 'h2' }) {
  const phone = useIsPhone()
  const names = s.state === 'online' ? (s.players?.names ?? []) : []
  return (
    <div className="flex flex-col gap-5 max-sm:gap-4">
      <article aria-labelledby={`server-${s.slug}`} className="overflow-hidden rounded-3xl border border-border bg-card shadow-card">
        <div className="flex items-start gap-5 p-8 max-sm:flex-col max-sm:gap-4 max-sm:p-5">
          <div className="flex min-w-0 flex-1 items-start gap-5 max-sm:gap-3.5">
            <Emblem size={phone ? 56 : 84} icon={serverPageIcon(s)} stopped={s.state === 'offline'} name={s.name} className="mt-0.5" />
            <div className="min-w-0 flex-1">
              <Heading id={`server-${s.slug}`} className={cn('font-extrabold tracking-[-0.03em] wrap-anywhere', Heading === 'h1' ? 'text-[40px] leading-[46px] max-sm:text-[26px] max-sm:leading-8' : 'text-[28px] leading-9 max-sm:text-[22px] max-sm:leading-7')}>
                {s.name}
              </Heading>
              {s.motd && <p className="mt-1.5 text-[17px] leading-6 whitespace-pre-line text-muted-foreground max-sm:mt-1 max-sm:text-[15px] max-sm:leading-[21px]">{s.motd}</p>}
            </div>
          </div>
          <Status server={s} />
        </div>
        <JoinBlock server={s} />
        <Facts server={s} />
      </article>
      {(s.stream || s.about) && (
        <div className={cn('grid gap-5 max-sm:grid-cols-1 max-sm:gap-4', s.stream && s.about ? 'grid-cols-[minmax(0,2fr)_minmax(0,1fr)]' : 'grid-cols-1')}>
          {s.stream && <WatchCard stream={s.stream} board={s.board} />}
          {s.about && <AboutCard text={s.about} />}
        </div>
      )}
      {s.board && (s.board.headline || s.board.stats?.length || s.board.checklist?.length) ? <ProgressCard board={s.board} /> : null}
      {names.length > 0 && <PlayingNow names={names} />}
    </div>
  )
}

const siteName: Record<PublicStream['site'], string> = { twitch: 'Twitch', youtube: 'YouTube' }

/**
 * The owner's live stream. Nothing loads from the stream's site until the
 * visitor presses Watch live; between sessions it counts down to the next.
 */
function WatchCard({ stream, board }: { stream: PublicStream; board?: PublicBoard }) {
  const [playing, setPlaying] = useState(false)
  const now = useNow(30_000)
  const site = siteName[stream.site]
  const live = board?.live ?? false
  const next = !live && board?.next && new Date(board.next).getTime() > now ? board.next : undefined
  return (
    <section aria-labelledby="watch-title" className="flex flex-col rounded-3xl border border-border bg-card p-6 shadow-card max-sm:p-5">
      <div className="flex items-center justify-between gap-4">
        <h2 id="watch-title" className="text-[15px] leading-5 font-semibold">
          {t('serverPage.watch')}
        </h2>
        {live && (
          <span className="inline-flex items-center gap-2 text-[13px] font-semibold text-destructive-foreground">
            <span className="size-2 animate-pulse rounded-full bg-destructive motion-reduce:animate-none" aria-hidden="true" />
            {t('serverPage.liveNow')}
          </span>
        )}
      </div>
      <div className="relative mt-4 aspect-video overflow-hidden rounded-2xl bg-[#0e3b21] text-white">
        {playing ? (
          <iframe src={streamEmbed(stream, window.location.hostname)} title={t('serverPage.playerTitle', { site, channel: stream.channel })} allow="autoplay; fullscreen" allowFullScreen className="absolute inset-0 size-full border-0" />
        ) : next ? (
          <div className="absolute inset-0 flex flex-col items-center justify-center px-6 text-center">
            <p className="text-sm font-medium text-white/70">{t('serverPage.nextSession')}</p>
            <p className="mt-1 text-[40px] leading-[46px] font-extrabold tracking-[-0.03em] tabular-nums max-sm:text-[30px] max-sm:leading-9">{untilText(next, now)}</p>
            <p className="mt-1.5 text-sm text-white/70">{t('serverPage.nextAt', { time: sessionTime(next) })}</p>
          </div>
        ) : (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
            <Pip pose="cheer" size={72} className="max-sm:hidden" />
            <Button size="lg" className="border-white bg-white text-[#0e3b21] shadow-none not-disabled:hover:bg-white/90" onClick={() => setPlaying(true)}>
              <PlayIcon />
              {t('serverPage.watchLive')}
            </Button>
            <p className="text-[13px] text-white/60">{t('serverPage.watchOn', { site })}</p>
          </div>
        )}
      </div>
      <a href={stream.url} target="_blank" rel="noreferrer noopener" className="mt-4 inline-flex items-center gap-1 self-start text-[15px] font-semibold text-success-strong hover:underline">
        {t('serverPage.follow', { site })}
        <ArrowUpRightIcon className="size-4" aria-hidden="true" />
      </a>
    </section>
  )
}

/** The owner's words for the page, as they wrote them. */
function AboutCard({ text }: { text: string }) {
  return (
    <section aria-labelledby="about-title" className="flex flex-col rounded-3xl border border-border bg-card p-6 shadow-card max-sm:p-5">
      <h2 id="about-title" className="text-[15px] leading-5 font-semibold">
        {t('serverPage.about')}
      </h2>
      <p className="mt-3 text-[15px] leading-6 whitespace-pre-line text-foreground/85 wrap-anywhere">{text}</p>
    </section>
  )
}

/** What the owner's tools last posted: a headline, a few numbers and a checklist. */
function ProgressCard({ board }: { board: PublicBoard }) {
  const now = useNow(60_000)
  const stats = board.stats ?? []
  const checklist = board.checklist ?? []
  return (
    <section aria-labelledby="progress-title" className="rounded-3xl border border-border bg-card p-6 shadow-card max-sm:p-5">
      <div className="flex items-baseline justify-between gap-4">
        <h2 id="progress-title" className="text-[15px] leading-5 font-semibold">
          {t('serverPage.progress')}
        </h2>
        <span className="text-xs text-muted-foreground">{t('serverPage.updated', { when: relativeTime(board.updatedAt, now) })}</span>
      </div>
      {board.headline && <p className="mt-1 text-[24px] leading-8 font-bold tracking-[-0.02em] wrap-anywhere max-sm:text-xl max-sm:leading-7">{board.headline}</p>}
      {stats.length > 0 && (
        <dl className={cn('mt-5 grid gap-px overflow-hidden rounded-2xl border border-border bg-border max-sm:grid-cols-2', { 1: 'grid-cols-1', 2: 'grid-cols-2', 3: 'grid-cols-3', 4: 'grid-cols-4', 5: 'grid-cols-5' }[stats.length] ?? 'grid-cols-6')}>
          {stats.map((st) => (
            <div key={st.label} className="min-w-0 bg-card px-4 py-3.5">
              <dt className="truncate text-xs font-medium text-muted-foreground">{st.label}</dt>
              <dd className="mt-0.5 truncate text-[22px] leading-7 font-bold tabular-nums">{st.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {checklist.length > 0 && (
        <ul className="mt-5 grid grid-cols-3 gap-x-6 gap-y-2.5 max-sm:grid-cols-1">
          {checklist.map((c) => (
            <li key={c.label} className="flex min-w-0 items-center gap-2.5 text-sm">
              {c.done ? (
                <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-success text-white" aria-hidden="true">
                  <CheckIcon className="size-3.5" strokeWidth={3} />
                </span>
              ) : (
                <span className="size-5 shrink-0 rounded-full border-[1.5px] border-muted-foreground/40" aria-hidden="true" />
              )}
              <span className={cn('truncate', c.done ? 'font-medium' : 'text-muted-foreground')}>{c.label}</span>
              <span className="sr-only">{c.done ? t('serverPage.done') : t('serverPage.notYet')}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function Status({ server: s }: { server: PublicServer }) {
  const st = publicStatus(s)
  return (
    <div className="flex shrink-0 flex-col items-end gap-0.5 pt-1.5 text-right max-sm:flex-row max-sm:items-center max-sm:gap-2 max-sm:pt-0 max-sm:text-left" role="status">
      <span className={cn('inline-flex items-center gap-2 text-[17px] leading-6 font-semibold max-sm:text-[15px] max-sm:leading-5', st.tone === 'online' ? 'text-success-foreground' : 'text-foreground')}>
        <Dot tone={st.tone} />
        {st.label}
      </span>
      {st.detail && (
        <span className="text-[15px] leading-5 text-muted-foreground tabular-nums max-sm:text-[13px] max-sm:leading-[18px]">
          <span className="sm:hidden" aria-hidden="true">
            ·{' '}
          </span>
          {st.detail}
        </span>
      )}
    </div>
  )
}

function JoinBlock({ server: s }: { server: PublicServer }) {
  const phone = useIsPhone()
  const steps = joinSteps(s)
  return (
    <section aria-label={t('serverPage.join')} className="border-t border-border bg-warm px-8 py-7 max-sm:px-5 max-sm:py-5">
      <div className="section-label">{t('serverPage.address')}</div>
      <div className="mt-2 flex items-center gap-4 max-sm:flex-col max-sm:items-stretch max-sm:gap-3">
        <p className="min-w-0 flex-1 text-[30px] leading-9 font-bold tracking-[-0.02em] break-all max-sm:text-[22px] max-sm:leading-7">{s.address}</p>
        <CopyButton text={s.address} label={t('serverPage.copy')} variant="default" size={phone ? 'touch' : 'lg'} className={phone ? 'w-full' : undefined} />
      </div>
      <ol className="mt-4 flex flex-col gap-1.5 text-[15px] leading-[21px] text-muted-foreground max-sm:mt-3.5">
        {steps.map((step, i) => (
          <li key={step} className="flex gap-2.5">
            <span className="inline-flex size-[21px] shrink-0 items-center justify-center rounded-full border border-border bg-white text-xs font-semibold text-foreground tabular-nums" aria-hidden="true">
              {i + 1}
            </span>
            <span className="min-w-0">{step}</span>
          </li>
        ))}
      </ol>
      {s.modpack && s.pack && (
        <a href={s.pack} className="mt-3.5 inline-flex items-center gap-1 text-[15px] font-semibold text-success-strong hover:underline">
          {t('serverPage.getPack', { pack: s.modpack.name })}
          <ArrowUpRightIcon className="size-4" aria-hidden="true" />
        </a>
      )}
    </section>
  )
}

function Facts({ server: s }: { server: PublicServer }) {
  const facts: { key: string; label: string; value: ReactNode }[] = [
    { key: 'version', label: t('serverPage.version'), value: <span className="tabular-nums">{s.minecraftVersion}</span> },
    {
      key: 'software',
      label: t('serverPage.software'),
      value: (
        <span className="inline-flex min-w-0 items-center gap-2">
          <TypeLogo type={s.type} size={26} />
          <span className="truncate">{typeName(s.type)}</span>
        </span>
      ),
    },
  ]
  if (s.modpack) {
    facts.push({
      key: 'modpack',
      label: t('serverPage.modpack'),
      value: (
        <span className="min-w-0">
          <span className="block truncate">{s.modpack.name}</span>
          {s.modpack.version && <span className="block truncate text-[13px] font-normal text-muted-foreground">{s.modpack.version}</span>}
        </span>
      ),
    })
  }
  if (s.map) {
    facts.push({
      key: 'map',
      label: t('serverPage.map'),
      value: (
        <a href={s.map} className="inline-flex items-center gap-1 text-success-strong hover:underline">
          {t('serverPage.openMap')}
          <ArrowUpRightIcon className="size-4" aria-hidden="true" />
        </a>
      ),
    })
  }
  return (
    <dl className={cn('grid border-t border-border max-sm:grid-cols-2', { 2: 'grid-cols-2', 3: 'grid-cols-3', 4: 'grid-cols-4' }[facts.length])}>
      {facts.map((f, i) => (
        <div key={f.key} className={cn('min-w-0 px-8 py-5 max-sm:px-5 max-sm:py-4', i > 0 && 'border-l border-border', i % 2 === 0 && 'max-sm:border-l-0', i > 1 && 'max-sm:border-t')}>
          <dt className="text-xs font-medium text-muted-foreground">{f.label}</dt>
          <dd className="mt-1 flex min-h-6 items-center text-[15px] font-semibold">{f.value}</dd>
        </div>
      ))}
    </dl>
  )
}

function PlayingNow({ names }: { names: string[] }) {
  return (
    <section aria-labelledby="playing-now" className="rounded-3xl border border-border bg-card p-6 shadow-card max-sm:p-5">
      <h2 id="playing-now" className="text-[15px] leading-5 font-semibold">
        {t('serverPage.playingNow')}
      </h2>
      <ul className="mt-4 grid grid-cols-4 gap-x-4 gap-y-3 max-sm:grid-cols-2">
        {names.map((n) => (
          <li key={n} className="flex min-w-0 items-center gap-2.5">
            <PlayerFace name={n} src={serverPageFace(n)} size={32} />
            <span className="truncate text-sm font-medium">{n}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** Playkeeper's footer: the call to action, with Pip waving on the site's pixel ground. */
function BrandBand() {
  const phone = useIsPhone()
  return (
    <section aria-labelledby="brand-band" className="relative overflow-hidden bg-[#0e3b21] text-white">
      <div className="mx-auto flex max-w-[920px] items-center gap-7 px-6 pt-10 pb-[104px] max-sm:flex-col max-sm:items-start max-sm:gap-4 max-sm:px-5 max-sm:pt-8 max-sm:pb-[92px]">
        <Pip pose="wave" size={phone ? 64 : 96} />
        <div className="min-w-0 flex-1">
          <p className="inline-flex items-center gap-2 text-[13px] font-semibold text-white/70">
            <BrandMark size={18} />
            {t('brand.name')}
          </p>
          <h2 id="brand-band" className="mt-1.5 text-[26px] leading-8 font-bold tracking-[-0.02em] max-sm:text-[22px] max-sm:leading-7">
            {t('serverPage.brandTitle')}
          </h2>
          <p className="mt-1 text-[15px] leading-[21px] text-pretty text-white/75">{t('serverPage.brandBody')}</p>
        </div>
        <Button size={phone ? 'touch' : 'lg'} className={cn('border-white bg-white text-[#0e3b21] shadow-none not-disabled:hover:bg-white/90 not-disabled:active:bg-white/85', phone && 'w-full')} render={<a href={siteLink} />}>
          {t('serverPage.brandCta')}
          <ArrowUpRightIcon />
        </Button>
      </div>
      <div className="pixelated absolute inset-x-0 bottom-0 h-[72px] bg-repeat-x" style={{ backgroundImage: `url(${ground})`, backgroundSize: '480px 72px', backgroundPosition: 'left bottom' }} aria-hidden="true" />
    </section>
  )
}

function Gone() {
  return (
    <div className="flex flex-1 animate-fade flex-col items-center justify-center py-16 text-center">
      <Pip pose="sleep" size={112} />
      <h1 className="mt-4 text-[28px] leading-9 font-bold tracking-[-0.02em] max-sm:text-2xl max-sm:leading-8">{t('serverPage.goneTitle')}</h1>
      <p className="mt-2 max-w-[440px] text-[15px] text-muted-foreground">{t('serverPage.goneBody')}</p>
    </div>
  )
}

function Loading() {
  return (
    <div className="overflow-hidden rounded-3xl border border-border bg-card shadow-card">
      <LoadingLabel />
      <div className="flex items-start gap-5 p-8 max-sm:p-5">
        <Skeleton className="size-[72px] rounded-[22%] max-sm:size-14" />
        <div className="flex flex-1 flex-col gap-2.5 pt-1">
          <Skeleton className="h-8 w-2/5" />
          <Skeleton className="h-5 w-3/5" />
        </div>
      </div>
      <div className="border-t border-border bg-warm px-8 py-7 max-sm:px-5">
        <Skeleton className="h-3 w-28" />
        <Skeleton className="mt-3 h-9 w-1/2" />
        <Skeleton className="mt-4 h-5 w-4/5" />
      </div>
      <div className="grid grid-cols-2 border-t border-border">
        <div className="px-8 py-5 max-sm:px-5">
          <Skeleton className="h-3 w-16" />
          <Skeleton className="mt-2 h-5 w-20" />
        </div>
        <div className="border-l border-border px-8 py-5 max-sm:px-5">
          <Skeleton className="h-3 w-16" />
          <Skeleton className="mt-2 h-5 w-24" />
        </div>
      </div>
    </div>
  )
}
