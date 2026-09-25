import { useState, type ReactNode } from 'react'
import { DownloadIcon, ExternalLinkIcon, FileDownIcon } from 'lucide-react'
import { usePackPage } from '@/api/packs'
import type { PackLauncher, PackPage as PackPageData, ShareYourself } from '@/api/types'
import { BrandMark, Emblem, Pip } from '@/components/app/art'
import { CopyButton, Marker } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { loaderLabel } from '@/components/app/modpacks'
import { CopyIconButton } from '@/components/app/pack-share'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { formatBytes } from '@/lib/format'
import { pageRows, shareText, siteLabel, yourselfLink, yourselfReason } from '@/lib/packs'
import { cn } from '@/lib/utils'

const enter = 'animate-in fade-in-0 duration-200 ease-out'

/**
 * The public friends' pack page at /packs/<token>. It needs no sign-in and
 * loads nothing from Modrinth or CurseForge. Every link that doesn't open a
 * shared pack shows the same page, which never names the server.
 */
export function PackPage({ token }: { token: string }) {
  const { state, retry } = usePackPage(token)
  switch (state.kind) {
    case 'loading':
      return (
        <PackFrame>
          <PageSkeleton />
        </PackFrame>
      )
    case 'ready':
      return <SharedPack page={state.page} token={token} />
    case 'gone':
      return (
        <PackFrame center>
          <Pip pose="sleep" size={112} />
          <h1 className="mt-4 text-2xl font-bold tracking-[-0.01em]">{t('packPage.goneTitle')}</h1>
          <p className="mt-2 text-[15px] text-muted-foreground">{t('packPage.goneBody')}</p>
        </PackFrame>
      )
    case 'busy':
      return (
        <PackFrame center>
          <Pip pose="hurt" size={112} />
          <h1 className="mt-4 text-2xl font-bold tracking-[-0.01em]">{t('packPage.busyTitle')}</h1>
          <p className="mt-2 text-[15px] text-muted-foreground">{t('packPage.busyBody')}</p>
          <Button variant="outline" className="mt-5" onClick={retry}>
            {t('common.tryAgain')}
          </Button>
        </PackFrame>
      )
    default: {
      const unreachable: never = state
      return unreachable
    }
  }
}

function PackFrame({ header, center, children }: { header?: ReactNode; center?: boolean; children: ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col bg-sidebar">
      {header}
      <main className={cn('flex w-full flex-1 flex-col px-6 max-sm:px-4', center ? 'items-center justify-center pb-16 text-center' : 'pb-12')}>{children}</main>
      <footer className="flex shrink-0 items-center justify-between gap-6 px-8 py-5 text-[11px] text-muted-foreground max-sm:flex-col max-sm:gap-1 max-sm:px-6 max-sm:pb-6 max-sm:text-center">
        <span className="inline-flex items-center gap-2 text-xs font-medium text-foreground/80">
          <BrandMark size={16} />
          {t('packPage.madeWith')}
        </span>
        <span>{t('footer.notOfficial')}</span>
      </footer>
    </div>
  )
}

function SharedPack({ page, token }: { page: PackPageData; token: string }) {
  const phone = useIsPhone()
  const { packMods, mods } = pageRows(page)
  const yourself = page.yourself ?? []
  const header = (
    <header className="flex items-center gap-3 px-8 pt-7 pb-6 max-sm:px-4 max-sm:pt-5 max-sm:pb-3">
      <Emblem size={36} icon={page.hasIcon ? `/packs/${token}/icon` : undefined} name={page.server} />
      <div className="min-w-0">
        <div className="truncate text-[17px] leading-6 font-bold">{page.server}</div>
        <div className="text-xs text-muted-foreground">{t('packPage.versions', { minecraft: page.minecraftVersion, loader: loaderLabel(page.loader, page.loaderVersion) })}</div>
      </div>
    </header>
  )
  return (
    <PackFrame header={header}>
      <div className={cn('mx-auto flex w-full max-w-[760px] flex-col gap-4 max-sm:gap-3', enter)}>
        <div>
          <h1 className="text-[28px] leading-9 font-bold tracking-[-0.02em] max-sm:text-[22px] max-sm:leading-7">{t('packPage.title', { server: page.server })}</h1>
          <p className="mt-1 text-[15px] text-muted-foreground">{t('packPage.lead', { notice: shareText(page.notice) })}</p>
        </div>
        <FileCard page={page} phone={phone} />
        <Section title={t('packPage.launchers')}>
          {phone ? (
            <PhoneLaunchers launchers={page.launchers} />
          ) : (
            <div className="grid grid-cols-2 gap-4">
              {page.launchers.map((l) => (
                <LauncherCard key={l.id} launcher={l} />
              ))}
            </div>
          )}
        </Section>
        {page.address && <JoinCard address={page.address} phone={phone} />}
        <Section title={t('packPage.get')}>
          <ul className="divide-y divide-border overflow-hidden rounded-2xl border border-border bg-card shadow-card">
            {page.pack && (
              <Row name={page.pack.name} version={page.pack.version} detail={t('packPage.packMods', { count: packMods })}>
                <Marker tone={page.pack.need === 'required' ? 'green' : 'muted'}>{shareText(page.pack.label)}</Marker>
              </Row>
            )}
            {mods.map((m) => (
              <Row key={`${m.name}-${m.version ?? ''}`} name={m.name} version={m.version} detail={m.neededBy ? t('packPage.neededBy', { mod: m.neededBy }) : undefined}>
                <Marker tone={m.need === 'required' ? 'green' : 'muted'}>{shareText(m.label)}</Marker>
              </Row>
            ))}
          </ul>
        </Section>
        {yourself.length > 0 && (
          <Section title={t('packPage.yourself')}>
            <ul className="divide-y divide-border overflow-hidden rounded-2xl border border-border bg-card shadow-card">
              {yourself.map((y) => (
                <Row key={y.path} name={y.name} detail={yourselfReason(y, 'page')}>
                  {y.page && <YourselfLink y={y} />}
                </Row>
              ))}
            </ul>
          </Section>
        )}
      </div>
    </PackFrame>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2.5">
      <h2 className="text-[15px] font-semibold">{title}</h2>
      {children}
    </section>
  )
}

function FileCard({ page, phone }: { page: PackPageData; phone: boolean }) {
  const download = (
    <Button size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} render={<a href={page.download.url} download={page.download.name} />}>
      <DownloadIcon />
      {t('common.download')}
    </Button>
  )
  return (
    <div className="flex items-center gap-3 rounded-2xl border border-border bg-card px-4 py-3.5 shadow-card max-sm:flex-col max-sm:items-stretch max-sm:gap-4 max-sm:p-4">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <FileDownIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold">{page.download.name}</div>
          <div className="text-xs text-muted-foreground">{t('packPage.fileHint', { size: formatBytes(page.download.size) })}</div>
        </div>
      </div>
      {download}
    </div>
  )
}

function LauncherSteps({ launcher }: { launcher: PackLauncher }) {
  return (
    <>
      <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-[13px] leading-[18px] marker:text-foreground">
        {launcher.steps.map((s) => (
          <li key={s.key} className="pl-1">
            {shareText(s)}
          </li>
        ))}
      </ol>
      <a href={launcher.site} target="_blank" rel="noreferrer noopener" aria-label={t('common.external', { label: siteLabel(launcher.site) })} className="mt-auto inline-flex items-center gap-1 self-start pt-4 text-xs text-success-strong hover:underline">
        {siteLabel(launcher.site)}
        <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
      </a>
    </>
  )
}

function LauncherCard({ launcher }: { launcher: PackLauncher }) {
  return (
    <div className="flex flex-col rounded-2xl border border-border bg-card p-5 shadow-card">
      <h3 className="mb-3 text-[15px] font-semibold">{launcher.name}</h3>
      <LauncherSteps launcher={launcher} />
    </div>
  )
}

function PhoneLaunchers({ launchers }: { launchers: PackLauncher[] }) {
  const [chosen, setChosen] = useState(launchers[0]?.id ?? '')
  const launcher = launchers.find((l) => l.id === chosen) ?? launchers[0]
  if (!launcher) return null
  return (
    <>
      <Segmented value={launcher.id} onChange={setChosen} options={launchers.map((l) => ({ value: l.id, label: l.name }))} label={t('packPage.launchers')} className="flex w-full" itemClassName="h-9 flex-1 text-sm" />
      <div className="flex flex-col rounded-2xl border border-border bg-card p-4 shadow-card">
        <LauncherSteps launcher={launcher} />
      </div>
    </>
  )
}

function JoinCard({ address, phone }: { address: string; phone: boolean }) {
  return (
    <div className="flex items-center gap-3 rounded-2xl border border-border bg-card px-4 py-3.5 shadow-card">
      <div className="min-w-0 flex-1">
        <div className="text-xs text-muted-foreground">{t('packPage.join')}</div>
        <div className="truncate text-lg font-semibold tracking-[-0.01em] max-sm:text-base">{address}</div>
      </div>
      {phone ? <CopyIconButton text={address} label={t('packPage.copyAddress')} variant="outline" size="icon-xl" className="rounded-xl" /> : <CopyButton text={address} variant="outline" />}
    </div>
  )
}

function Row({ name, version, detail, children }: { name: string; version?: string; detail?: string; children?: ReactNode }) {
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="text-sm leading-5">
          <span className="font-semibold">{name}</span>
          {version && <span className="ml-1.5 text-xs text-muted-foreground">{version}</span>}
        </div>
        {detail && <div className="text-xs leading-4 text-muted-foreground">{detail}</div>}
      </div>
      <div className="shrink-0 text-right">{children}</div>
    </li>
  )
}

function YourselfLink({ y }: { y: ShareYourself }) {
  const label = yourselfLink(y)
  return (
    <a href={y.page} target="_blank" rel="noreferrer noopener" aria-label={t('common.external', { label: t('packs.linkFor', { link: label, name: y.name }) })} className="inline-flex items-center gap-1 text-xs font-medium text-success-strong hover:underline">
      {label}
      <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
    </a>
  )
}

function PageSkeleton() {
  return (
    <div className="mx-auto mt-24 flex w-full max-w-[760px] flex-col gap-4 max-sm:mt-20" aria-busy="true" aria-label={t('common.loading')}>
      <Skeleton className="h-9 w-3/5" />
      <Skeleton className="h-5 w-2/5" />
      <Skeleton className="mt-2 h-[70px] w-full rounded-2xl" />
      <div className="grid grid-cols-2 gap-4 max-sm:grid-cols-1">
        <Skeleton className="h-48 rounded-2xl" />
        <Skeleton className="h-48 rounded-2xl max-sm:hidden" />
      </div>
      <Skeleton className="h-[70px] w-full rounded-2xl" />
    </div>
  )
}
