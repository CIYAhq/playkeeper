import { useState, type ReactNode } from 'react'
import { ChevronRightIcon, CircleArrowUpIcon, DownloadIcon, EllipsisIcon, ExternalLinkIcon, RotateCwIcon, SearchIcon, Trash2Icon } from 'lucide-react'
import { useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Marker, Notice, SectionLabel } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PackModsSection } from '@/components/app/pack-mods'
import { PackShareNotice } from '@/components/app/pack-share'
import { Button } from '@/components/ui/button'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { packFiles, pendingCount, sourceNames, updatableRows, updateKeys, type AddonRow } from '@/lib/addons'
import { formatList } from '@/lib/format'
import { linkProps, type Route } from '@/lib/router'
import { cn } from '@/lib/utils'
import { AddonIcon, motion, rowDomId, useAddons } from './state'

export function InstalledView() {
  const a = useAddons()
  const phone = useIsPhone()
  const browse: Route = { name: 'server', slug: a.server.slug, tab: a.tab, sub: 'browse' }
  const browseLabel = a.kind === 'mod' ? t('addons.browseMods') : t('addons.browse')

  if (a.loading) return <InstalledSkeleton phone={phone} />
  if (!a.addons) {
    return (
      <Notice tone="error" title={a.error?.message ?? ''} action={<Button variant="outline" onClick={() => void a.refresh()}>{t('common.tryAgain')}</Button>}>
        {a.error?.hint}
      </Notice>
    )
  }
  if (a.rows.length === 0 && !a.addons.modpack) {
    return (
      <div className={cn('flex flex-1 flex-col items-center justify-center py-16 text-center', motion.fade)}>
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
  const busyOp = !!a.server.operation
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
  return { updatable, restartLine, restart, updateAll, busy, busyOp }
}

function DesktopList({ browse, browseLabel }: { browse: Route; browseLabel: string }) {
  const a = useAddons()
  const { updatable, restartLine, restart, updateAll, busy, busyOp } = useListActions()
  return (
    <section className="flex flex-col gap-4" aria-labelledby="addons-title">
      {a.kind === 'mod' && <PackShareNotice server={a.server} />}
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="addons-title" className="mr-auto text-lg font-bold tracking-[-0.01em]">
          {a.kind === 'mod' ? t('addons.titleMods', { server: a.server.name }) : t('addons.title', { server: a.server.name })}
        </h2>
        {updatable.length >= 2 && (
          <Button variant="outline" onClick={() => void updateAll()} loading={busy === 'updates'} disabled={busyOp}>
            <CircleArrowUpIcon />
            {t('addons.updateAll')}
          </Button>
        )}
        <Button render={<a {...linkProps(browse)} />}>
          <SearchIcon />
          {browseLabel}
        </Button>
      </div>
      {restartLine && (
        <div className={cn('flex items-center gap-3', motion.enter)} role="status">
          <span className="size-2 shrink-0 rounded-full bg-warning" aria-hidden="true" />
          <p className="min-w-0 flex-1 text-sm font-semibold">{restartLine}</p>
          <Button variant="outline" size="sm" onClick={() => void restart()} loading={busy === 'restart'} disabled={busyOp}>
            <RotateCwIcon />
            {t('addons.restartNow')}
          </Button>
        </div>
      )}
      {a.addons?.modpack && a.rows.length > 0 && <SectionLabel className="-mb-2">{t('packMods.addedByYou')}</SectionLabel>}
      {a.rows.length > 0 && (
        <ul className="overflow-hidden rounded-2xl border border-border bg-card shadow-card">
          {a.rows.map((r) => (
            <DesktopRow key={r.id} row={r} />
          ))}
        </ul>
      )}
      {a.addons?.modpack && <PackModsSection server={a.server} pack={a.addons.modpack} files={packFiles(a.addons)} folder={a.addons.target.folder} phone={false} />}
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

function DesktopRow({ row: r }: { row: AddonRow }) {
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
      className={cn(
        'grid min-h-16 grid-cols-[minmax(0,1fr)_88px_minmax(0,230px)_32px] items-center gap-4 border-b border-border px-4 py-2.5 transition-colors duration-300 last:border-b-0 xl:grid-cols-[minmax(0,1fr)_110px_330px_32px]',
        openable(r) && 'hover:bg-accent/40',
        a.highlight === r.id && 'bg-warning/10',
        motion.fade,
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
  const [busy, setBusy] = useState(false)
  const run = async (fn: () => Promise<boolean>) => {
    setBusy(true)
    await fn()
    setBusy(false)
  }
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
              <Button variant="ghost" size="sm" onClick={() => void run(() => a.forget(key))} disabled={busy}>
                {t('addons.forget')}
              </Button>
              <Button variant="outline" size="sm" onClick={() => void run(() => a.update([key], t('addons.installing', { name: r.name })))} disabled={busy || !!a.server.operation}>
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
          <Button variant="outline" size="sm" onClick={() => void run(() => a.adopt(r.fileName))} loading={busy}>
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
  return (
    <Menu>
      <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('addons.menuFor', { name: r.name })} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-52">
        {installed && update && (
          <MenuItem
            disabled={!!a.server.operation}
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
  const { updatable, restartLine, restart, updateAll, busy, busyOp } = useListActions()
  return (
    <div className="flex flex-col gap-4 pb-20">
      {a.kind === 'mod' && <PackShareNotice server={a.server} />}
      {restartLine && (
        <div className={cn('flex items-center gap-3', motion.enter)} role="status">
          <p className="min-w-0 flex-1 text-[15px] leading-5 font-semibold">{restartLine}</p>
          <Button variant="outline" className="h-11 rounded-xl px-3.5 text-[15px]" onClick={() => void restart()} loading={busy === 'restart'} disabled={busyOp}>
            <RotateCwIcon />
            {t('server.restart')}
          </Button>
        </div>
      )}
      {updatable.length >= 2 && (
        <div className={cn('flex items-center gap-3', motion.enter)}>
          <div className="min-w-0 flex-1">
            <p className="text-[15px] leading-5 font-semibold">{t('addons.updates', { count: updatable.length })}</p>
            <p className="truncate text-[13px] text-muted-foreground">{formatList(updatable.map((r) => r.name))}</p>
          </div>
          <Button variant="outline" className="h-11 rounded-xl px-3.5 text-[15px]" onClick={() => void updateAll()} loading={busy === 'updates'} disabled={busyOp}>
            <CircleArrowUpIcon />
            {t('addons.updateAll')}
          </Button>
        </div>
      )}
      {a.rows.length > 0 && (
        <section>
          <SectionLabel className="px-4 pb-2">{a.addons?.modpack ? t('packMods.addedByYou') : t('addons.onServer', { server: a.server.name })}</SectionLabel>
          <ul className="overflow-hidden rounded-3xl border border-border bg-white">
            {a.rows.map((r) => (
              <PhoneRow key={r.id} row={r} />
            ))}
          </ul>
        </section>
      )}
      {a.addons?.modpack && <PackModsSection server={a.server} pack={a.addons.modpack} files={packFiles(a.addons)} folder={a.addons.target.folder} phone />}
      <div className="fixed inset-x-0 bottom-[calc(52px+env(safe-area-inset-bottom))] z-30 bg-gradient-to-t from-sidebar via-sidebar/95 to-sidebar/0 px-4 pt-4 pb-3">
        <Button size="touch" className="w-full" render={<a {...linkProps(browse)} />}>
          <SearchIcon />
          {browseLabel}
        </Button>
      </div>
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

function PhoneRow({ row: r }: { row: AddonRow }) {
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
    <li id={rowDomId(r.id)} className={cn('border-b border-border last:border-b-0', a.highlight === r.id && 'bg-warning/10', motion.fade)}>
      {openable(r) ? (
        <button type="button" onClick={() => openRow(a, r)} className={cn('flex min-h-14 w-full items-center gap-3 px-3 py-2 text-left active:bg-accent/60', motion.press)}>
          {body}
          <PhoneMark row={r} />
        </button>
      ) : (
        <div className="flex min-h-14 items-center gap-3 px-3 py-2">{body}</div>
      )}
    </li>
  )
}

function InstalledSkeleton({ phone }: { phone: boolean }) {
  return (
    <div className="flex flex-col gap-4" aria-busy="true">
      {!phone && (
        <div className="flex items-center justify-between">
          <Skeleton className="h-6 w-56" />
          <Skeleton className="h-8 w-36 rounded-lg" />
        </div>
      )}
      <div className={cn('overflow-hidden border border-border bg-card', phone ? 'rounded-3xl' : 'rounded-2xl')}>
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="flex min-h-16 items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
            <Skeleton className="size-10 rounded-[10px]" />
            <div className="flex-1">
              <Skeleton className="h-3.5 w-40" />
              <Skeleton className="mt-2 h-3 w-64 max-w-full" />
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
