import { useEffect, useState, type ReactNode } from 'react'
import { ArrowRightIcon, CircleArrowUpIcon, DownloadIcon, ExternalLinkIcon, Trash2Icon } from 'lucide-react'
import { ApiError, get } from '@/api/client'
import type { AddonBrowse, AddonCard, AddonDetails } from '@/api/types'
import { Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { alsoInstalls, compactCount, footerFor, keyFrom, libraryMatch, searchPath, sourceNames, updatedAgo, versionPage } from '@/lib/addons'
import { busyReason, opLabel } from '@/lib/phase'
import { navigate } from '@/lib/router'
import { softwareLabel } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { AddonIcon, detailsPath, useAddons, type Detail } from './state'

/** The add-on's details: a sheet on the right, or from the bottom on phone. */
export function DetailSheet() {
  const a = useAddons()
  const phone = useIsPhone()
  // The last add-on stays in the sheet while it slides out.
  const [last, setLast] = useState<Detail>()
  useEffect(() => {
    if (a.detail) setLast(a.detail)
  }, [a.detail])
  const shown = a.detail ?? last
  return (
    <Sheet open={!!a.detail} onOpenChange={(open) => !open && a.openDetail(undefined)}>
      <SheetPopup
        side={phone ? 'bottom' : 'right'}
        showCloseButton={!phone}
        className={phone ? 'max-h-[calc(100dvh-48px)]' : 'm-2 max-w-[440px] rounded-2xl border before:hidden'}
      >
        {shown && <DetailBody detail={shown} />}
      </SheetPopup>
    </Sheet>
  )
}

function asApiError(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' })
}

/** The details the sheet opened with, or fetched for it. */
function useDetails(serverId: string, detail: Detail) {
  const [res, setRes] = useState<{ for: Detail; details?: AddonDetails; error?: ApiError }>()
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (detail.details) return
    let stale = false
    get<AddonDetails>(detailsPath(serverId, detail.key))
      .then((details) => !stale && setRes({ for: detail, details }))
      .catch((e: unknown) => !stale && setRes({ for: detail, error: asApiError(e) }))
    return () => {
      stale = true
    }
  }, [serverId, detail, attempt])
  const mine = res?.for === detail ? res : undefined
  return {
    details: detail.details ?? mine?.details,
    error: mine?.error,
    retry: () => {
      setRes(undefined)
      setAttempt((n) => n + 1)
    },
  }
}

function DetailBody({ detail }: { detail: Detail }) {
  const a = useAddons()
  const phone = useIsPhone()
  const { details: d, error, retry } = useDetails(a.server.id, detail)
  const pad = phone ? 'px-5' : 'px-6'

  if (error) {
    return (
      <div className={cn('pt-6 pb-8', pad)}>
        <SheetTitle className="sr-only">{error.message}</SheetTitle>
        <Notice tone="error" title={error.message} action={<Button variant="outline" onClick={retry}>{t('common.tryAgain')}</Button>}>
          {error.hint}
        </Notice>
      </div>
    )
  }
  if (!d) return <DetailSkeleton phone={phone} />

  const software = softwareLabel(a.server)
  const inst = d.installed
  const byHand = detail.adoptFile ? a.rows.find((r) => r.fileName === detail.adoptFile) : undefined
  const update = d.updateAvailable ? d.latest : undefined
  const also = byHand ? [] : alsoInstalls(d)
  const whatsNew = inst && update ? versionPage(d.card, update) : undefined
  const rows: [string, ReactNode][] = []
  if (d.card.license && !(phone && (inst || byHand))) rows.push([t('addons.licence'), d.card.license])
  if (inst) {
    rows.push([t('addons.installed'), t('addons.installedFrom', { version: inst.versionNumber, source: sourceNames[inst.source] })])
    if (update) rows.push([t('addons.newest'), t('addons.versionFor', { version: update.versionNumber, software })])
  } else if (byHand) {
    rows.push([t('addons.installed'), [byHand.version, t('addons.addedByHand')].filter(Boolean).join(t('common.dot'))])
    if (d.latest && d.latest.versionNumber !== byHand.version) rows.push([t('addons.newest'), t('addons.versionFor', { version: d.latest.versionNumber, software })])
  } else {
    rows.push([t('addons.downloads'), compactCount(d.card.downloads)])
  }
  if (d.card.updated) rows.push([t('addons.lastUpdated'), updatedAgo(d.card.updated)])
  if (!inst && !byHand && !phone && d.latest) rows.push([t('addons.version'), t('addons.versionFor', { version: d.latest.versionNumber, software })])
  const warnings = inst || detail.adoptFile ? [] : (d.plan?.warnings ?? [])

  return (
    <div className="flex min-h-0 flex-1 animate-fade flex-col">
      <div className={cn('flex items-center gap-3.5', pad, phone ? 'pt-3' : 'pt-6 pr-14')}>
        <AddonIcon url={d.card.iconUrl} size={56} className="rounded-[14px]" />
        <div className="min-w-0">
          <SheetTitle className="truncate text-xl leading-7 font-bold">{d.card.name}</SheetTitle>
          {d.card.author && <p className="truncate text-[13px] text-muted-foreground">{t('addons.by', { author: d.card.author })}</p>}
        </div>
      </div>
      <SheetPanel className={cn('pt-4 pb-4', pad)}>
        {d.card.summary && <SheetDescription className="text-sm leading-5 text-foreground">{d.card.summary}</SheetDescription>}
        <dl className="mt-4">
          {rows.map(([label, value]) => (
            <div key={label} className="flex min-h-10 items-center gap-4 border-b border-border py-2 text-[13px]">
              <dt className="w-24 shrink-0 text-muted-foreground">{label}</dt>
              <dd className="min-w-0 font-medium break-words">{value}</dd>
            </div>
          ))}
        </dl>
        {also.length > 0 && (
          <div className="mt-4">
            <h3 className="text-[13px] font-semibold">{t('addons.alsoInstalls')}</h3>
            <ul className="mt-0.5 text-[13px] text-muted-foreground">
              {also.map((s) => (
                <li key={`${s.source}:${s.projectId}`}>{`${s.name} ${s.versionNumber}`}</li>
              ))}
            </ul>
          </div>
        )}
        {warnings.length > 0 && (
          <ul className="mt-4 flex flex-col gap-1 text-[13px] text-warning-foreground">
            {warnings.map((w) => (
              <li key={`${w.kind}:${w.message}`}>{w.message}</li>
            ))}
          </ul>
        )}
        {!phone && (whatsNew || d.card.pageUrl) && (
          <div className="mt-4 flex flex-col items-start gap-3">
            {whatsNew && update && <SourceLink href={whatsNew}>{t('addons.whatsNew', { version: update.versionNumber })}</SourceLink>}
            {d.card.pageUrl && <SourceLink href={d.card.pageUrl}>{t('addons.openSourcePage')}</SourceLink>}
          </div>
        )}
      </SheetPanel>
      <DetailFooter d={d} adoptFile={detail.adoptFile} />
    </div>
  )
}

function SourceLink({ href, children }: { href: string; children: string }) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-[13px] font-medium text-success-strong hover:underline" aria-label={t('common.external', { label: children })}>
      {children}
      <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
    </a>
  )
}

/** A reason there's no Install: a bold line, why, and where to go instead. */
function Blocked({ title, body, children }: { title: string; body?: string; children?: ReactNode }) {
  return (
    <>
      <div>
        <p className="text-sm font-semibold">{title}</p>
        {body && <p className="mt-1.5 text-xs text-muted-foreground">{body}</p>}
      </div>
      {children}
    </>
  )
}

/** A dependency another site names, looked up in the library: its card, null when it isn't there, undefined while looking. */
function useLibraryMatch(serverId: string, name: string): AddonCard | null | undefined {
  const [found, setFound] = useState<{ name: string; card: AddonCard | null }>()
  useEffect(() => {
    if (!name) return
    let stale = false
    get<AddonBrowse>(searchPath(serverId, { q: name, category: '', sort: 'downloads' }))
      .then((res) => !stale && setFound({ name, card: libraryMatch(res.cards, name) ?? null }))
      .catch(() => !stale && setFound({ name, card: null }))
    return () => {
      stale = true
    }
  }, [serverId, name])
  return name && found?.name === name ? found.card : undefined
}

function DetailFooter({ d, adoptFile }: { d: AddonDetails; adoptFile?: string }) {
  const a = useAddons()
  const phone = useIsPhone()
  const f = footerFor(d)
  const inLibrary = useLibraryMatch(a.server.id, !adoptFile && f.kind === 'needs' && f.external ? f.dependency : '')
  const [busy, setBusy] = useState<'install' | 'update' | 'adopt' | 'forget'>()
  const run = async (what: NonNullable<typeof busy>, fn: () => Promise<boolean>) => {
    setBusy(what)
    await fn()
    setBusy(undefined)
  }
  const key = keyFrom(d.card)
  const name = d.card.name
  const op = a.server.operation
  const blocked = busyReason(a.server)
  const size = phone ? 'touch' : 'lg'
  // Installs and updates wait for the server's current job; the line under
  // the button says which.
  const caption = (restart: boolean) => {
    const text = op ? opLabel(op, a.server.name) : restart ? t('addons.loadsAfterRestart') : ''
    return text ? <p className="text-center text-xs text-muted-foreground">{text}</p> : null
  }

  let body: ReactNode
  if (adoptFile) {
    body = (
      <Button size={size} className="w-full" onClick={() => void run('adopt', () => a.adopt(adoptFile))} loading={busy === 'adopt'}>
        {t('addons.manage')}
      </Button>
    )
  } else {
    switch (f.kind) {
      case 'install':
        body = (
          <>
            <Button size={size} className="w-full" onClick={() => void run('install', () => a.install(key, name, f.fingerprint))} loading={busy === 'install'} disabledReason={blocked}>
              <DownloadIcon />
              <span className="truncate">{t('addons.install', { name })}</span>
            </Button>
            {phone && !op ? null : caption(true)}
          </>
        )
        break
      case 'external':
        body = (
          <Blocked title={t('addons.onlyAuthorSite')} body={a.kind === 'mod' ? t('addons.onlyAuthorSiteBodyMods') : t('addons.onlyAuthorSiteBody')}>
            {f.url && (
              <Button variant="outline" size={size} className="w-full" render={<a href={f.url} target="_blank" rel="noreferrer" aria-label={t('common.external', { label: t('addons.downloadFromAuthor') })} />}>
                {t('addons.downloadFromAuthor')}
                <ExternalLinkIcon />
              </Button>
            )}
          </Blocked>
        )
        break
      case 'conflict':
        body = (
          <Blocked title={t('addons.conflictTitle', { other: f.other })} body={t('addons.conflictBody', { other: f.other })}>
            <Button variant="outline" size={size} className="w-full" onClick={() => a.goToFile(f.file, f.other)}>
              <span className="truncate">{t('addons.goTo', { other: f.other })}</span>
              <ArrowRightIcon />
            </Button>
          </Blocked>
        )
        break
      case 'needs':
        body = inLibrary ? (
          <Blocked title={t('addons.needsTitle', { dependency: f.dependency })} body={t('addons.needsInLibraryBody', { dependency: inLibrary.name })}>
            <Button variant="outline" size={size} className="w-full" onClick={() => a.openDetail({ key: keyFrom(inLibrary) })}>
              <span className="truncate">{t('addons.goTo', { other: inLibrary.name })}</span>
              <ArrowRightIcon />
            </Button>
          </Blocked>
        ) : f.external && inLibrary === undefined ? (
          <Blocked title={t('addons.needsTitle', { dependency: f.dependency })} />
        ) : (
          <Blocked title={t('addons.needsTitle', { dependency: f.dependency })} body={t('addons.needsBody', { dependency: f.dependency })}>
            {f.url && (
              <Button
                variant="outline"
                size={size}
                className="w-full"
                render={<a href={f.url} target="_blank" rel="noreferrer" aria-label={t('common.external', { label: t('addons.getFromAuthor', { dependency: f.dependency }) })} />}
              >
                <span className="truncate">{t('addons.getFromAuthor', { dependency: f.dependency })}</span>
                <ExternalLinkIcon />
              </Button>
            )}
          </Blocked>
        )
        break
      case 'installed': {
        if (f.missing) {
          body = (
            <>
              <div className={cn('flex gap-2', phone && 'flex-col-reverse')}>
                <Button variant="outline" size={size} className={phone ? 'w-full' : ''} onClick={() => void run('forget', () => a.forget(key))} loading={busy === 'forget'}>
                  {t('addons.forget')}
                </Button>
                <Button size={size} className={phone ? 'w-full' : 'flex-1'} onClick={() => void run('update', () => a.update([key], t('addons.installing', { name })))} loading={busy === 'update'} disabledReason={blocked}>
                  <DownloadIcon />
                  {t('addons.reinstall')}
                </Button>
              </div>
              {caption(true)}
            </>
          )
          break
        }
        const up = f.update
        const remove = (
          <Button variant="destructive-outline" size={size} className={up ? (phone ? 'w-full' : '') : 'w-full'} onClick={() => a.askRemove(key)}>
            <Trash2Icon />
            {t('common.remove')}
          </Button>
        )
        body = up ? (
          <>
            <div className={cn('flex gap-2', phone && 'flex-col-reverse')}>
              {remove}
              <Button
                size={size}
                className={phone ? 'w-full' : 'flex-1'}
                onClick={() =>
                  f.changed ? a.askUpdate({ key, name, version: up.versionNumber }) : void run('update', () => a.update([key], t('addons.updatingOne', { name })))
                }
                loading={busy === 'update'}
                disabledReason={blocked}
              >
                <CircleArrowUpIcon />
                <span className="truncate">{t('addons.updateTo', { version: up.versionNumber })}</span>
              </Button>
            </div>
            {caption(true)}
          </>
        ) : (
          <>
            {remove}
            {caption(false)}
          </>
        )
        break
      }
      case 'map':
        body = (
          <Blocked title={t('addons.usedByMap')} body={t('addons.usedByMapBody')}>
            <Button
              variant="outline"
              size={size}
              className="w-full"
              onClick={() => {
                a.openDetail(undefined)
                navigate({ name: 'server', slug: a.server.slug, tab: 'map' })
              }}
            >
              <span className="truncate">{t('addons.openMap')}</span>
              <ArrowRightIcon />
            </Button>
          </Blocked>
        )
        break
      case 'blocked':
        body = f.notice ? <Blocked title={f.notice.message} body={f.notice.hint} /> : null
        break
      default: {
        const unreachable: never = f
        body = unreachable
      }
    }
  }
  if (!body) return null
  return <div className={cn('flex flex-col gap-2.5 pt-3', phone ? 'px-5 pb-2' : 'px-6 pb-6')}>{body}</div>
}

function DetailSkeleton({ phone }: { phone: boolean }) {
  return (
    <div className={cn('flex flex-col', phone ? 'px-5 pt-3 pb-6' : 'px-6 pt-6 pb-6')} aria-busy="true">
      <SheetTitle className="sr-only">{t('common.loading')}</SheetTitle>
      <div className="flex items-center gap-3.5">
        <Skeleton className="size-14 rounded-[14px]" />
        <div className="flex-1">
          <Skeleton className="h-5 w-40" />
          <Skeleton className="mt-2 h-3 w-24" />
        </div>
      </div>
      <Skeleton className="mt-5 h-3.5 w-full" />
      <Skeleton className="mt-2 h-3.5 w-3/4" />
      <div className="mt-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="flex min-h-10 items-center gap-4 border-b border-border py-2">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-3 w-24" />
          </div>
        ))}
      </div>
    </div>
  )
}
