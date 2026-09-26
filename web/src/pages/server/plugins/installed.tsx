import { useState, type ReactNode } from 'react'
import { ChevronRightIcon, CircleArrowUpIcon, DownloadIcon, EllipsisIcon, ExternalLinkIcon, RotateCwIcon, SearchIcon, Trash2Icon } from 'lucide-react'
import { useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Marker, Notice, SectionLabel } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { t } from '@/i18n'
import { pendingCount, sourceNames, updatableRows, updateKeys, type AddonRow } from '@/lib/addons'
import { formatList } from '@/lib/format'
import { busyReason } from '@/lib/phase'
import { presenceProps, useListPresence, type Presence } from '@/lib/presence'
import { linkProps, type Route } from '@/lib/router'
import { cn } from '@/lib/utils'
import { AddonIcon, rowDomId, useAddons } from './state'

const rowId = (r: AddonRow) => r.id

export function InstalledView() {
  const a = useAddons()
  const phone = useIsPhone()
  const browse: Route = { name: 'server', slug: a.server.slug, tab: a.tab, sub: 'browse' }
  const browseLabel = a.kind === 'mod' ? t('addons.browseMods') : t('addons.browse')

  if (a.loading) return <InstalledSkeleton phone={phone} browse={browse} browseLabel={browseLabel} />
  if (!a.addons) {
    return (
      <Notice tone="error" title={a.error?.message ?? ''} action={<Button variant="outline" onClick={() => void a.refresh()}>{t('common.tryAgain')}</Button>}>
        {a.error?.hint}
      </Notice>
    )
  }
  if (a.rows.length === 0) {
    return (
      <div className="flex flex-1 animate-fade flex-col items-center justify-center py-16 text-center">
        <Pip pose="search" size={96} />
        <h2 className="mt-4 text-xl font-bold">{a.kind === 'mod' ? t('addons.emptyTitleMods') : t('addons.emptyTitle')}</h2>
        <p className="mt-1 max-w-[380px] text-sm text-muted-foreground">{t('addons.emptyBody')}</p>
        <Button className="mt-5" size={phone ? 'touch' : 'default'} render={<a {...linkProps(browse)} />}>
          <SearchIcon />
          {browseLabel}
        </Button>
      </div>
    )
  }
  return phone ? <PhoneList browse={browse} browseLabel={browseLabel} /> : <DesktopList browse={browse} browseLabel={browseLabel} />
}

function useListActions() {
  const a = useAddons()
  const ws = useWorkspace()
  const [busy, setBusy] = useState<'restart' | 'updates'>()
  const updatable = updatableRows(a.rows)
  const pending = pendingCount(a.rows)
  const running = !ws.stale && a.server.phase === 'online'
  const restartLine = running && pending > 0 ? t(a.kind === 'mod' ? 'addons.restartToLoadMods' : 'addons.restartToLoad', { server: a.server.name, count: pending }) : undefined
  const blocked = busyReason(a.server)
  const restart = async () => {
    setBusy('restart')
    await a.restart()
    setBusy(undefined)
  }
  const updateAll = async () => {
    setBusy('updates')
    await a.update(updateKeys(a.rows), t(a.kind === 'mod' ? 'addons.updatingManyMods' : 'addons.updatingMany', { count: updatable.length }))
    setBusy(undefined)
  }
  return { updatable, restartLine, restart, updateAll, busy, blocked }
}

function DesktopHeading({ browse, browseLabel, children }: { browse: Route; browseLabel: string; children?: ReactNode }) {
  const a = useAddons()
  return (
    <div className="flex flex-wrap items-center gap-2">
      <h2 id="addons-title" className="mr-auto text-lg font-bold tracking-[-0.01em]">
        {a.kind === 'mod' ? t('addons.titleMods', { server: a.server.name }) : t('addons.title', { server: a.server.name })}
      </h2>
      {children}
      <Button render={<a {...linkProps(browse)} />}>
        <SearchIcon />
        {browseLabel}
      </Button>
    </div>
  )
}

function DesktopList({ browse, browseLabel }: { browse: Route; browseLabel: string }) {
  const a = useAddons()
  const { updatable, restartLine, restart, updateAll, busy, blocked } = useListActions()
  const rows = useListPresence(a.rows, rowId)
  return (
    <section className="flex flex-col gap-4" aria-labelledby="addons-title">
      <DesktopHeading browse={browse} browseLabel={browseLabel}>
        {updatable.length >= 2 && (
          <Button variant="outline" className="animate-fade" onClick={() => void updateAll()} loading={busy === 'updates'} disabledReason={blocked}>
            <CircleArrowUpIcon />
            {t('addons.updateAll')}
          </Button>
        )}
      </DesktopHeading>
      {restartLine && (
        <div className="flex animate-enter items-center gap-3" role="status">
          <span className="size-2 shrink-0 rounded-full bg-warning" aria-hidden="true" />
          <p className="min-w-0 flex-1 text-sm font-semibold">{restartLine}</p>
          <Button variant="outline" size="sm" onClick={() => void restart()} loading={busy === 'restart'} disabledReason={blocked}>
            <RotateCwIcon />
            {t('addons.restartNow')}
          </Button>
        </div>
      )}
      <ul className="overflow-hidden rounded-2xl border border-border bg-card shadow-card">
        {rows.map((p) => (
          <DesktopRow key={p.key} row={p.item} presence={p.state} />
        ))}
      </ul>
    </section>
  )
}

function openable(r: AddonRow): boolean {
  return r.state !== 'unknown' && !!r.addon
}

function openRow(a: ReturnType<typeof useAddons>, r: AddonRow) {
  if (!r.addon) return
  a.openDetail(r.state === 'identified' ? { key: r.addon, adoptFile: r.fileName } : { key: r.addon })
}

function DesktopRow({ row: r, presence }: { row: AddonRow; presence: Presence }) {
  const a = useAddons()
  const main = (
    <>
      <AddonIcon url={r.addon?.iconUrl} dim={r.state === 'missing'} />
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="truncate text-sm font-semibold">{r.name}</span>
          {r.version && <span className="shrink-0 text-xs text-muted-foreground">{r.version}</span>}
        </span>
        <span className="block truncate text-[13px] text-muted-foreground">{r.state === 'unknown' ? r.fileName : r.addon?.summary}</span>
      </span>
    </>
  )
  return (
    <li
      id={rowDomId(r.id)}
      {...presenceProps(presence)}
      className={cn(
        'grid min-h-16 grid-cols-[minmax(0,1fr)_88px_minmax(0,230px)_32px] items-center gap-4 border-b border-border px-4 py-2.5 transition-colors duration-(--motion-standard) ease-standard last:border-b-0 xl:grid-cols-[minmax(0,1fr)_110px_330px_32px]',
        openable(r) && 'hover:bg-accent/40',
        a.highlight === r.id && 'bg-warning/10',
      )}
    >
      {openable(r) ? (
        <button type="button" onClick={() => openRow(a, r)} className="-m-1 flex min-w-0 items-center gap-3 rounded-lg p-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {main}
        </button>
      ) : (
        <div className="flex min-w-0 items-center gap-3">{main}</div>
      )}
      <span className="truncate text-[13px] text-muted-foreground">{r.addon && (r.state === 'managed' || r.state === 'changed' || r.state === 'missing') ? sourceNames[r.addon.source] : t('addons.byHand')}</span>
      <RowStatus row={r} />
      <RowMenu row={r} />
    </li>
  )
}

function TwoLines({ first, second, className }: { first: string; second: string; className?: string }) {
  return (
    <span className="min-w-0 flex-1 leading-[18px]">
      <span className={cn('block truncate text-[13px] font-semibold', className)}>{first}</span>
      <span className="block truncate text-xs text-muted-foreground">{second}</span>
    </span>
  )
}

function RowStatus({ row: r }: { row: AddonRow }) {
  const a = useAddons()
  const [busy, setBusy] = useState<'forget' | 'reinstall' | 'adopt'>()
  const run = async (what: NonNullable<typeof busy>, fn: () => Promise<boolean>) => {
    setBusy(what)
    await fn()
    setBusy(undefined)
  }
  const waitFor = (what: NonNullable<typeof busy>) => (busy === what ? t('reason.busy', { what: what === 'forget' ? t('addons.forgetting', { name: r.name }) : t('addons.installing', { name: r.name }) }) : undefined)
  let body: ReactNode
  switch (r.state) {
    case 'managed':
      body = r.update ? (
        <Marker tone="green">{t('addons.updateAvailable', { version: r.update.versionNumber })}</Marker>
      ) : r.pending ? (
        <Marker tone="amber">{t('addons.new')}</Marker>
      ) : (
        <Marker>{t('addons.upToDate')}</Marker>
      )
      break
    case 'changed':
      body = <TwoLines first={t('addons.changed')} second={t('addons.asksFirst')} className="text-warning-foreground" />
      break
    case 'missing': {
      const key = r.addon
      body = (
        <>
          <span className="min-w-0 flex-1 truncate text-[13px] font-semibold text-destructive-foreground">{t('addons.fileMissing')}</span>
          {key && (
            <>
              <Button variant="ghost" size="sm" onClick={() => void run('forget', () => a.forget(key))} loading={busy === 'forget'} disabledReason={waitFor('reinstall')}>
                {t('addons.forget')}
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void run('reinstall', () => a.update([key], t('addons.installing', { name: r.name })))}
                loading={busy === 'reinstall'}
                disabledReason={waitFor('forget') ?? busyReason(a.server)}
              >
                <DownloadIcon />
                {t('addons.reinstall')}
              </Button>
            </>
          )}
        </>
      )
      break
    }
    case 'identified':
      body = (
        <>
          <TwoLines first={t('addons.addedByHand')} second={t('addons.foundOnModrinth')} />
          <Button variant="outline" size="sm" onClick={() => void run('adopt', () => a.adopt(r.fileName))} loading={busy === 'adopt'}>
            {t('addons.manage')}
          </Button>
        </>
      )
      break
    case 'unknown':
      body = <TwoLines first={t('addons.addedByHand')} second={t('addons.notInLibrary')} />
      break
    default: {
      const unreachable: never = r.state
      body = unreachable
    }
  }
  return <div className="flex min-w-0 items-center gap-2">{body}</div>
}

function RowMenu({ row: r }: { row: AddonRow }) {
  const a = useAddons()
  const key = r.addon
  if (!key || r.state === 'unknown') return <span />
  const installed = r.state === 'managed' || r.state === 'changed'
  const update = r.update
  const blocked = busyReason(a.server)
  return (
    <Menu>
      <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('addons.menuFor', { name: r.name })} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-52">
        {installed && update && (
          <MenuItem
            disabled={!!blocked}
            title={blocked}
            onClick={() => (r.state === 'changed' ? a.askUpdate({ key, name: r.name, version: update.versionNumber }) : void a.update([key], t('addons.updatingOne', { name: r.name })))}
          >
            <CircleArrowUpIcon />
            {t('addons.updateTo', { version: update.versionNumber })}
          </MenuItem>
        )}
        <MenuItem onClick={() => a.openSource(key)}>
          <ExternalLinkIcon />
          {t('addons.openSource')}
        </MenuItem>
        {installed && (
          <>
            <MenuSeparator />
            <MenuItem variant="destructive" onClick={() => a.askRemove(key)}>
              <Trash2Icon />
              {t('common.remove')}
            </MenuItem>
          </>
        )}
      </MenuPopup>
    </Menu>
  )
}

function PhoneList({ browse, browseLabel }: { browse: Route; browseLabel: string }) {
  const a = useAddons()
  const { updatable, restartLine, restart, updateAll, busy, blocked } = useListActions()
  const rows = useListPresence(a.rows, rowId)
  return (
    <div className="flex flex-col gap-4 pb-20">
      {restartLine && (
        <div className="flex animate-enter items-center gap-3" role="status">
          <p className="min-w-0 flex-1 text-[15px] leading-5 font-semibold">{restartLine}</p>
          <Button variant="outline" className="h-11 rounded-xl px-3.5 text-[15px]" onClick={() => void restart()} loading={busy === 'restart'} disabledReason={blocked}>
            <RotateCwIcon />
            {t('server.restart')}
          </Button>
        </div>
      )}
      {updatable.length >= 2 && (
        <div className="flex animate-enter items-center gap-3">
          <div className="min-w-0 flex-1">
            <p className="text-[15px] leading-5 font-semibold">{t('addons.updates', { count: updatable.length })}</p>
            <p className="truncate text-[13px] text-muted-foreground">{formatList(updatable.map((r) => r.name))}</p>
          </div>
          <Button variant="outline" className="h-11 rounded-xl px-3.5 text-[15px]" onClick={() => void updateAll()} loading={busy === 'updates'} disabledReason={blocked}>
            <CircleArrowUpIcon />
            {t('addons.updateAll')}
          </Button>
        </div>
      )}
      <section>
        <SectionLabel className="px-4 pb-2">{t('addons.onServer', { server: a.server.name })}</SectionLabel>
        <ul className={phoneListClass}>
          {rows.map((p) => (
            <PhoneRow key={p.key} row={p.item} presence={p.state} />
          ))}
        </ul>
      </section>
      <PhoneBrowse browse={browse} browseLabel={browseLabel} />
    </div>
  )
}

const phoneListClass = 'overflow-hidden rounded-3xl border border-border bg-white'
const phoneRowClass = 'flex min-h-14 w-full items-center gap-3 px-3 py-2'

function PhoneBrowse({ browse, browseLabel }: { browse: Route; browseLabel: string }) {
  return (
    <div className="fixed inset-x-0 bottom-[calc(52px+env(safe-area-inset-bottom))] z-30 bg-gradient-to-t from-sidebar via-sidebar/95 to-sidebar/0 px-4 pt-4 pb-3">
      <Button size="touch" className="w-full" render={<a {...linkProps(browse)} />}>
        <SearchIcon />
        {browseLabel}
      </Button>
    </div>
  )
}

function phoneLine(r: AddonRow): string {
  switch (r.state) {
    case 'managed':
      return [r.version, r.addon ? sourceNames[r.addon.source] : ''].filter(Boolean).join(t('common.dot'))
    case 'changed':
      return t('addons.changed')
    case 'missing':
      return t('addons.fileMissing')
    case 'identified':
      return [t('addons.addedByHand'), t('addons.foundOnModrinth')].join(t('common.dot'))
    case 'unknown':
      return t('addons.addedByHand')
    default: {
      const unreachable: never = r.state
      return unreachable
    }
  }
}

function PhoneMark({ row: r }: { row: AddonRow }) {
  const cls = 'shrink-0 text-[13px]'
  if (r.state === 'changed') return <Marker tone="amber" className={cls}>{t('addons.markChanged')}</Marker>
  if (r.state === 'missing') return <Marker tone="red" className={cls}>{t('addons.markMissing')}</Marker>
  if (r.state === 'identified') return <Marker tone="green" className={cls}>{t('addons.markManage')}</Marker>
  if (r.update) return <Marker tone="green" className={cls}>{t('addons.markUpdate')}</Marker>
  if (r.pending) return <Marker tone="amber" className={cls}>{t('addons.new')}</Marker>
  return <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
}

function PhoneRow({ row: r, presence }: { row: AddonRow; presence: Presence }) {
  const a = useAddons()
  const body = (
    <>
      <AddonIcon url={r.addon?.iconUrl} dim={r.state === 'missing'} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-base leading-5">{r.name}</span>
        <span className="block truncate text-[13px] text-muted-foreground">{phoneLine(r)}</span>
      </span>
    </>
  )
  return (
    <li id={rowDomId(r.id)} {...presenceProps(presence)} className={cn('border-b border-border transition-colors duration-(--motion-standard) ease-standard last:border-b-0', a.highlight === r.id && 'bg-warning/10')}>
      {openable(r) ? (
        <button type="button" onClick={() => openRow(a, r)} className={cn(phoneRowClass, 'text-left')}>
          {body}
          <PhoneMark row={r} />
        </button>
      ) : (
        <div className={phoneRowClass}>{body}</div>
      )}
    </li>
  )
}

/** The list's heading straight away, and grey rows where the add-ons will be. */
function InstalledSkeleton({ phone, browse, browseLabel }: { phone: boolean; browse: Route; browseLabel: string }) {
  const a = useAddons()
  if (phone) {
    return (
      <div className="flex flex-col gap-4 pb-20">
        <section>
          <SectionLabel className="px-4 pb-2">{t('addons.onServer', { server: a.server.name })}</SectionLabel>
          <ListSkeleton rows={4} face="size-10 rounded-[10px]" className={phoneListClass} rowClassName={cn(phoneRowClass, 'border-b border-border last:border-b-0')} />
        </section>
        <PhoneBrowse browse={browse} browseLabel={browseLabel} />
      </div>
    )
  }
  return (
    <section className="flex flex-col gap-4" aria-labelledby="addons-title">
      <DesktopHeading browse={browse} browseLabel={browseLabel} />
      <ListSkeleton rows={4} face="size-10 rounded-[10px]" className="overflow-hidden rounded-2xl border border-border bg-card shadow-card" rowClassName="flex min-h-16 items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0" />
    </section>
  )
}
