import { useEffect, useState, type ReactNode } from 'react'
import { ArrowUpRightIcon } from 'lucide-react'
import { get } from '@/api/client'
import type { PublicPage, PublicServer } from '@/api/types'
import ground from '@/assets/pixel-art/ground.svg'
import { BrandMark, Emblem, Pip, TypeLogo } from '@/components/app/art'
import { CopyButton, Dot, PlayerFace } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { joinSteps, publicStatus, serverPageApi, serverPageFace, serverPageIcon, serverPageTitle } from '@/lib/server-page'
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
      {names.length > 0 && <PlayingNow names={names} />}
    </div>
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
