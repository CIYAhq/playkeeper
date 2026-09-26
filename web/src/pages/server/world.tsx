import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArchiveIcon, ArrowRightIcon, ChevronDownIcon, ChevronRightIcon, ChevronUpIcon, CopyIcon, DownloadIcon, EllipsisIcon, HistoryIcon, PencilIcon, RotateCcwIcon, ShieldCheckIcon, SlidersHorizontalIcon, Trash2Icon, UploadIcon } from 'lucide-react'
import { ApiError, del, get, post } from '@/api/client'
import type { Backup, RestorePreview, ServerStatus, WorldCopy } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { EmptyArt, Pip } from '@/components/app/art'
import { BackupRefusedNotice } from '@/components/app/backup-refused'
import { Card, CardHint, CardTitle, copyText, Notice, SectionLabel } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { FailedJobNotice, SavingPausedNotice } from '@/components/app/notices'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { InlineSkeleton, ListSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatBytes, formatDate, formatDay, formatMs, relativeTime } from '@/lib/format'
import { busyReason, failedJob, whyNot } from '@/lib/phase'
import { presenceProps, useListPresence, type Presence } from '@/lib/presence'
import { linkPath, linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { CopyRestoreDialog, CopyRow, phoneStored, storedCell, storedRows, useCopyRestore, useStoredCopies, type StoredRow } from './copy-restore'
import { phoneRow, PhoneWorldLinks, WorldLinks, WorldTools } from './world-links'

const newestShown = 6

const rowKey = (r: StoredRow) => (r.kind === 'here' ? r.backup.id : `copy:${r.copy.name}`)

function downloadURL(s: ServerStatus, b: Backup): string {
  return serverApi(s.id, `/backups/${b.id}/download`)
}

function madeOnline(b: Backup): boolean {
  return b.method === 'online_copy' || b.method === 'online_in_place'
}

/** "Players stay online", with about how long the newest backup made that way took. */
function onlineBody(backups: Backup[]): string {
  const last = backups.find((b) => madeOnline(b) && b.durationMs > 0)
  if (!last) return t('world.makeOnline')
  const seconds = Math.max(5, Math.ceil(last.durationMs / 5000) * 5)
  if (seconds < 60) return t('world.makeOnlineSeconds', { count: seconds })
  return t('world.makeOnlineMinutes', { count: Math.max(1, Math.round(last.durationMs / 60_000)) })
}

/**
 * World saving paused, scheduled backups refused since the last backup, a
 * backup that just failed, or a world a restore left behind, above the rest.
 * Refused scheduled backups stay until a backup succeeds.
 */
function WorldNotice({ server: s, className }: { server: ServerStatus; className?: string }) {
  const { stale } = useWorkspace()
  const [dismissed, setDismissed] = useState<string>()
  if (stale) return null
  const refused = s.backupRefused
  const refusedNotice = refused && <BackupRefusedNotice server={s} refusal={refused} className={className} />
  if (s.savingPausedSince)
    return (
      <>
        <SavingPausedNotice server={s} className={className} />
        {refusedNotice}
      </>
    )
  const failed = failedJob(s)
  return (
    <>
      {refusedNotice}
      {failed?.kind === 'backup' && failed.id !== refused?.operationId && dismissed !== failed.id && <FailedJobNotice server={s} op={failed} onDismiss={() => setDismissed(failed.id)} className={className} />}
      <LeftoverCopy server={s} className={className} />
    </>
  )
}

/** Backups, restores (their rollback archive) and version updates add a backup when they finish, not when they're asked for. */
function useReloadAfterJobs(s: ServerStatus, reload: () => Promise<void>) {
  const job = s.operation?.id
  const last = useRef(job)
  useEffect(() => {
    if (last.current && last.current !== job) void reload()
    last.current = job
  }, [job, reload])
}

export function WorldPage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const backups = usePoll(() => get<Backup[]>(serverApi(s.id, '/backups')), 10_000, s.id)
  useReloadAfterJobs(s, backups.refresh)
  const [preview, setPreview] = useState<RestorePreview>()
  const [restoreSheet, setRestoreSheet] = useState(false)
  const [showAll, setShowAll] = useState(false)
  const newest = backups.data?.[0]?.id
  const refresh = () => void backups.refresh()
  const stored = useStoredCopies(s.id)
  const copies = stored.data?.copies ?? []
  const copiesOn = !!stored.data?.view.enabled
  const place = stored.data?.view.place ?? ''
  const all = useMemo(() => storedRows(backups.data ?? [], stored.data?.copies ?? [], stored.data?.view), [backups.data, stored.data])
  const rows = useListPresence(backups.data ? (showAll ? all : all.slice(0, newestShown)) : undefined, rowKey)
  const restore = useCopyRestore(s, setPreview)
  const jobDialog = <CopyRestoreDialog restore={restore} copies={copies} place={place} />
  const more =
    all.length > newestShown ? (
      <button type="button" onClick={() => setShowAll(!showAll)} className="inline-flex min-h-8 items-center gap-1 self-start rounded-md text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring max-sm:px-4">
        {showAll ? t('world.showFewer') : t('world.showAll', { count: all.length })}
        {showAll ? <ChevronUpIcon className="size-3.5" aria-hidden="true" /> : <ChevronDownIcon className="size-3.5" aria-hidden="true" />}
      </button>
    ) : null

  async function restoreFrom(b: Backup) {
    try {
      setPreview(await post<RestorePreview>(serverApi(s.id, `/backups/${b.id}/restore`)))
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  const dialog = <RestoreDialog preview={preview} server={s} onClose={() => setPreview(undefined)} />

  if (backups.data && all.length === 0) {
    return (
      <>
        <WorldNotice server={s} className="max-sm:px-1" />
        <EmptyBackups server={s} phone={phone} />
        {dialog}
        {jobDialog}
      </>
    )
  }

  if (phone) {
    return (
      <div className="flex flex-col gap-4">
        <WorldNotice server={s} className="px-1" />
        <MakeBackup server={s} backups={backups.data ?? []} phone onDone={refresh} />
        <section aria-labelledby="backups">
          <SectionLabel className="px-4">
            <span id="backups">{copiesOn ? t('world.phoneListCopies', { place }) : t('world.listPhone')}</span>
          </SectionLabel>
          {backups.data ? (
            <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
              {rows.map(({ key, item: r, state }) =>
                r.kind === 'there' ? (
                  <li key={key} {...presenceProps(state)} className="flex min-h-[70px] items-center gap-3 border-b border-border py-2 pr-3 pl-4 last:border-b-0">
                    <span className="min-w-0 flex-1">
                      <span className="block text-base">{formatDay(r.copy.createdAt)}</span>
                      <span className="block text-[13px] text-muted-foreground">{[formatBytes(r.copy.sizeBytes), phoneStored(r, place)].join(t('common.dot'))}</span>
                    </span>
                    {can(ws.me, 'backups.restore') && (
                      <Button size="lg" variant="outline" onClick={() => void restore.start(r.copy)}>
                        <RotateCcwIcon />
                        {t('world.restoreCopyShort')}
                      </Button>
                    )}
                  </li>
                ) : (
                  <li key={key} {...presenceProps(state)} className="flex min-h-[70px] items-center gap-3 border-b border-border py-2 pr-3 pl-4 last:border-b-0">
                    <span className="min-w-0 flex-1">
                      <span className="block text-base">{formatDay(r.backup.createdAt)}</span>
                      <span className="block text-[13px] text-muted-foreground">{[r.backup.note, formatBytes(r.backup.sizeBytes), phoneStored(r, place) ?? (r.backup.downloadedAt ? t('world.phoneDownloaded') : t('world.phoneNotDownloaded'))].filter(Boolean).join(t('common.dot'))}</span>
                    </span>
                    <Button size="lg" variant={r.backup.id === newest && !r.backup.downloadedAt && !copiesOn ? 'default' : 'outline'} render={<a href={downloadURL(s, r.backup)} download={r.backup.fileName} onClick={() => window.setTimeout(refresh, 3000)} />}>
                      <DownloadIcon />
                      {t('common.download')}
                    </Button>
                  </li>
                ),
              )}
            </ul>
          ) : (
            <ListSkeleton rowClassName="flex min-h-[70px] items-center gap-3 border-b border-border py-2 pr-3 pl-4 last:border-b-0" className="mt-2 overflow-hidden rounded-3xl border border-border bg-white" trailing={<Skeleton className="h-10 w-28 shrink-0 rounded-lg" />} />
          )}
          {more && <div className="mt-2 flex">{more}</div>}
        </section>
        <ul className="overflow-hidden rounded-3xl border border-border bg-white">
          <li className="border-b border-border">
            <a {...linkProps({ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-rules' })} className={phoneRow}>
              <SlidersHorizontalIcon aria-hidden="true" />
              <span className="min-w-0 flex-1 text-base">{t('world.rules')}</span>
              <ChevronRightIcon aria-hidden="true" />
            </a>
          </li>
          <li className="border-b border-border">
            <button type="button" onClick={() => setRestoreSheet(true)} className={phoneRow}>
              <RotateCcwIcon aria-hidden="true" />
              <span className="min-w-0 flex-1 text-base">{t('world.restorePhone')}</span>
              <ChevronRightIcon aria-hidden="true" />
            </button>
          </li>
          <PhoneWorldLinks server={s} />
        </ul>
        <Sheet open={restoreSheet} onOpenChange={setRestoreSheet}>
          <SheetPopup side="bottom">
            <div className="px-5 pt-3">
              <SheetTitle className="text-lg font-bold">{t('world.restore')}</SheetTitle>
            </div>
            <SheetPanel className="flex flex-col gap-3 px-5 pt-4">
              <ul className="overflow-hidden rounded-2xl border border-border">
                {all.map((r) => {
                  const hint = r.kind === 'there' ? phoneStored(r, place) : r.backup.note
                  return (
                    <li key={r.kind === 'there' ? r.copy.name : r.backup.id} className="border-b border-border last:border-b-0">
                      <button
                        type="button"
                        className="flex min-h-14 w-full items-center gap-3 px-4 text-left"
                        onClick={() => {
                          setRestoreSheet(false)
                          void (r.kind === 'there' ? restore.start(r.copy) : restoreFrom(r.backup))
                        }}
                      >
                        <HistoryIcon className="size-5 text-muted-foreground" aria-hidden="true" />
                        <span className="min-w-0 flex-1">
                          <span className="block text-base">{formatDay(r.kind === 'there' ? r.copy.createdAt : r.backup.createdAt)}</span>
                          {hint && <span className="block truncate text-[13px] text-muted-foreground">{hint}</span>}
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
              <RestoreDropZone
                server={s}
                compact
                onPreview={(p) => {
                  setRestoreSheet(false)
                  setPreview(p)
                }}
              />
            </SheetPanel>
          </SheetPopup>
        </Sheet>
        {dialog}
        {jobDialog}
      </div>
    )
  }

  return (
    <>
      <WorldNotice server={s} />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <MakeBackup server={s} backups={backups.data ?? []} onDone={refresh} />
        <WorldInfo server={s} backups={backups.data} />
      </div>
      <section aria-labelledby="backups" className="mt-2 flex flex-col">
        <div className="flex items-end justify-between gap-4">
          <div className="min-w-0">
            <h2 id="backups" className="text-[15px] font-semibold">
              {t('world.list')}
            </h2>
            <p className="mt-0.5 text-xs text-muted-foreground">{copiesOn ? t('world.listHintCopies', { place }) : t('world.listHint')}</p>
          </div>
          <a {...linkProps({ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-rules' })} className="inline-flex shrink-0 items-center gap-1 rounded-md text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
            {t('world.rules')}
            <ArrowRightIcon className="size-3.5" aria-hidden="true" />
          </a>
        </div>
        <div className="mt-3 overflow-x-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-labelledby="backups">
          <table className="w-full min-w-[680px] text-[13px]">
            <thead className="bg-muted text-left text-xs text-muted-foreground">
              <tr className="h-9">
                <th className="px-3 font-medium">{t('world.col.made')}</th>
                <th className="px-3 text-right font-medium">{t('world.col.size')}</th>
                <th className="px-3 font-medium">{t('world.col.check')}</th>
                <th className="px-3 font-medium">{t('world.col.stored')}</th>
                <th className="px-3 text-right font-medium">{t('world.col.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {!backups.data && <TableSkeleton cols={['start', 'end', 'start', 'start', 'end']} rowClassName="h-12 border-t border-border" />}
              {rows.map(({ key, item: r, state }) =>
                r.kind === 'there' ? (
                  <CopyRow key={key} server={s} row={r} state={state} place={place} onRestore={() => void restore.start(r.copy)} onChanged={() => void stored.refresh()} />
                ) : (
                  <BackupRow key={key} server={s} backup={r.backup} state={state} newest={r.backup.id === newest} copiesOn={copiesOn} stored={storedCell(r, place)} onRestore={() => void restoreFrom(r.backup)} onChanged={refresh} />
                ),
              )}
            </tbody>
          </table>
        </div>
        {more && <div className="mt-2 flex">{more}</div>}
      </section>
      <Card className="mt-2">
        <CardTitle>{t('world.restore')}</CardTitle>
        <div className="mt-4 grid grid-cols-1 items-center gap-5 md:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]">
          <RestoreDropZone server={s} onPreview={setPreview} />
          <p className="text-[13px] text-muted-foreground">{t('world.restoreNote')}</p>
        </div>
      </Card>
      {ws.stale ? null : dialog}
      {ws.stale ? null : jobDialog}
    </>
  )
}

function MakeBackup({ server: s, backups, phone, onDone }: { server: ServerStatus; backups: Backup[]; phone?: boolean; onDone: () => void }) {
  const ws = useWorkspace()
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const running = s.operation?.kind === 'backup'
  const online = s.phase === 'online'
  const blocked = whyNot(s, 'change', ws.stale)
  if (!can(ws.me, 'backups.make')) return null

  async function backup() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/backups'), note.trim() ? { note: note.trim() } : {})
      setNote('')
      onDone()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const button = (size: 'default' | 'touch') => (
    <Button size={size} onClick={backup} loading={busy || running} disabledReason={blocked} className={size === 'touch' ? 'w-full' : undefined}>
      <ArchiveIcon />
      {running ? t('world.backingUp') : t('world.backUpNow')}
    </Button>
  )

  if (phone) {
    return (
      <Card className="p-4">
        <div className="flex items-start gap-3">
          <Pip pose="letter" size={52} />
          <div className="min-w-0">
            <CardTitle className="text-[17px]">{t('world.make')}</CardTitle>
            <p className="mt-1 text-[15px] leading-5 text-muted-foreground">{online ? t('world.makeOnline') : t('world.makeBodyStopped', { server: s.name })}</p>
          </div>
        </div>
        <div className="mt-4">{button('touch')}</div>
      </Card>
    )
  }
  return (
    <Card>
      <CardTitle>{t('world.make')}</CardTitle>
      {/* Centred in whatever height the World card next to it gives this one. */}
      <div className="my-auto flex items-center gap-4 py-4">
        <Pip pose="letter" size={52} />
        <p className="min-w-0 text-[13px] leading-[18px]">{online ? onlineBody(backups) : t('world.makeBodyStopped', { server: s.name })}</p>
      </div>
      <div className="flex gap-2 pt-1">
        <InputGroup className="flex-1">
          <InputGroupAddon>
            <PencilIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('world.notePlaceholder')} aria-label={t('world.noteLabel')} maxLength={120} disabled={!!blocked} />
        </InputGroup>
        {button('default')}
      </div>
    </Card>
  )
}

function WorldInfo({ server: s, backups }: { server: ServerStatus; backups: Backup[] | undefined }) {
  return (
    <Card>
      <CardTitle>{t('world.info')}</CardTitle>
      <CardHint>{t('world.infoMeta', { level: s.config?.levelName ?? 'world', version: s.config?.minecraftVersion ?? '' })}</CardHint>
      <dl className="mt-4 grid grid-cols-3 gap-3 border-b border-border pb-4">
        <div>
          <dt className="text-xs text-muted-foreground">{t('world.size')}</dt>
          <dd className="mt-0.5 text-lg font-bold">{s.worldBytes !== undefined ? formatBytes(s.worldBytes) : '—'}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">{t('world.created')}</dt>
          <dd className="mt-0.5 text-lg font-bold">{formatDate(s.createdAt)}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">{t('world.backups')}</dt>
          <dd className="mt-0.5 text-lg font-bold tabular-nums">{backups ? backups.length : <InlineSkeleton className="h-5 w-6" />}</dd>
        </div>
      </dl>
      <ul className="mt-3 flex flex-col">
        <WorldLinks server={s} />
        <li>
          <a
            {...linkPath('/servers/new#world')}
            className="group -mx-2 flex items-center gap-3 rounded-lg px-2 py-1 outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring active:bg-accent [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground"
          >
            <UploadIcon />
            <span className="min-w-0 flex-1">
              <span className="block text-[13px] font-semibold">{t('world.ownWorld')}</span>
              <span className="block text-xs text-muted-foreground">{t('world.ownWorldHint')}</span>
            </span>
            <ChevronRightIcon className="transition-transform duration-(--motion-fast) ease-standard group-hover:translate-x-0.5" aria-hidden="true" />
          </a>
        </li>
      </ul>
    </Card>
  )
}

function BackupRow({ server: s, backup: b, state, newest, copiesOn, stored, onRestore, onChanged }: { server: ServerStatus; backup: Backup; state: Presence; newest: boolean; copiesOn?: boolean; stored?: ReactNode; onRestore: () => void; onChanged: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)

  async function verify() {
    try {
      const r = await post<Backup>(serverApi(s.id, `/backups/${b.id}/verify`))
      toastManager.add(r.verified ? { title: t('world.checkedToast'), type: 'success' } : { title: t('world.checkFailedToast', { error: r.verifyError ?? '' }), type: 'error' })
      onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await del(serverApi(s.id, `/backups/${b.id}`))
      setConfirm(false)
      onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const kind = b.kind === 'manual' ? t('world.manual') : t('world.rollback')
  const downtime = madeOnline(b) ? t('world.noDowntime') : b.downtimeMs > 0 ? t('world.offline', { time: formatMs(b.downtimeMs) }) : undefined
  const detail = [kind, downtime, t('unit.files', { count: b.fileCount })].filter(Boolean).join(t('common.dot'))
  const when = formatDay(b.createdAt)
  return (
    <tr {...presenceProps(state)} className="h-12 border-t border-border">
      <td className="px-3 py-2">
        <span className="block">
          <span className="font-semibold">{when}</span>
          {b.note && (
            <span className="text-muted-foreground">
              {t('common.dot')}
              {b.note}
            </span>
          )}
        </span>
        <span className="block text-xs text-muted-foreground">{detail}</span>
      </td>
      <td className="px-3 text-right tabular-nums">{formatBytes(b.sizeBytes)}</td>
      <td className={cn('px-3 font-medium', b.verified ? 'text-success-foreground' : b.verifyError ? 'text-destructive-foreground' : 'text-muted-foreground')}>{b.verified ? t('world.verified') : b.verifyError ? t('world.failed') : t('world.unchecked')}</td>
      <td className="px-3">
        {stored ??
          (b.downloadedAt ? (
            <>
              <span className="block">{t('world.onVps')}</span>
              <span className="block text-xs text-muted-foreground">{t('world.downloadedAt', { time: relativeTime(b.downloadedAt) })}</span>
            </>
          ) : (
            <>
              <span className="block font-medium text-warning-foreground">{t('world.onlyHere')}</span>
              <span className="block text-xs text-muted-foreground">{t('world.notDownloaded')}</span>
            </>
          ))}
      </td>
      <td className="px-3">
        <span className="flex items-center justify-end gap-1">
          <Button size="sm" variant={newest && !b.downloadedAt && !copiesOn ? 'default' : 'outline'} render={<a href={downloadURL(s, b)} download={b.fileName} onClick={() => window.setTimeout(onChanged, 3000)} />}>
            <DownloadIcon />
            {t('common.download')}
          </Button>
          <Menu>
            <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('world.menuFor', { time: when })} />}>
              <EllipsisIcon />
            </MenuTrigger>
            <MenuPopup align="end" className="min-w-60">
              <MenuItem onClick={onRestore} className="items-start py-1.5">
                <HistoryIcon className="mt-0.5" />
                <span>
                  <span className="block">{t('world.restoreThis')}</span>
                  <span className="block text-xs text-muted-foreground">{t('world.restoreThisHint')}</span>
                </span>
              </MenuItem>
              <MenuItem onClick={() => void verify()}>
                <ShieldCheckIcon />
                {t('world.checkAgain')}
              </MenuItem>
              <MenuItem
                onClick={async () => {
                  const ok = await copyText(b.sha256)
                  toastManager.add(ok ? { title: t('world.checksumCopied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
                }}
                className="items-start py-1.5"
              >
                <CopyIcon className="mt-0.5" />
                <span>
                  <span className="block">{t('world.copyChecksum')}</span>
                  <span className="block font-mono text-xs text-muted-foreground">{t('world.checksum', { short: `${b.sha256.slice(0, 6)}…${b.sha256.slice(-4)}` })}</span>
                </span>
              </MenuItem>
              <MenuSeparator />
              <MenuItem variant="destructive" onClick={() => setConfirm(true)}>
                <Trash2Icon />
                {t('world.deleteBackup')}
              </MenuItem>
            </MenuPopup>
          </Menu>
        </span>
        <Dialog open={confirm} onOpenChange={setConfirm}>
          <DialogPopup className="sm:max-w-[460px]">
            <DialogHeader>
              <DialogTitle className="text-lg font-bold">{t('world.deleteTitle')}</DialogTitle>
              <DialogDescription>{t('world.deleteBody', { time: when })}</DialogDescription>
            </DialogHeader>
            <DialogFooter variant="bare" className="border-t border-border pt-4">
              <Button variant="ghost" onClick={() => setConfirm(false)}>
                {t('common.cancel')}
              </Button>
              <Button variant="destructive" onClick={remove} loading={busy}>
                <Trash2Icon />
                {t('world.deleteConfirm')}
              </Button>
            </DialogFooter>
          </DialogPopup>
        </Dialog>
      </td>
    </tr>
  )
}

function EmptyBackups({ server: s, phone }: { server: ServerStatus; phone: boolean }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const running = s.operation?.kind === 'backup'
  const online = s.phase === 'online'
  const steps = [
    { title: t('world.emptyStep1'), hint: t('world.emptyStep1Hint') },
    { title: t('world.emptyStep2'), hint: t('world.emptyStep2Hint') },
    { title: t('world.emptyStep3'), hint: t('world.emptyStep3Hint') },
  ]
  return (
    <div className="flex flex-1 flex-col items-center py-6 text-center max-sm:py-2">
      <EmptyArt kind="backups" scale={phone ? 6 : 5} />
      <h2 className="mt-5 text-title font-extrabold tracking-[-0.015em] max-sm:text-[22px]">{t('world.emptyTitle')}</h2>
      <p className="mt-2 max-w-[520px] text-sm text-muted-foreground max-sm:text-[15px]">{t('world.emptyBody')}</p>
      {can(ws.me, 'backups.make') && (
        <Button
          size={phone ? 'touch' : 'lg'}
          className="mt-5 max-sm:w-full"
          loading={busy || running}
          disabledReason={whyNot(s, 'change', ws.stale)}
          onClick={async () => {
            setBusy(true)
            try {
              await post(serverApi(s.id, '/backups'))
            } catch (e) {
              toastManager.add({ title: errorText(e), type: 'error' })
            } finally {
              setBusy(false)
            }
          }}
        >
          <ArchiveIcon />
          {running ? t('world.backingUp') : t('world.emptyButton')}
        </Button>
      )}
      <p className="mt-3 text-xs text-muted-foreground">{online ? t('world.emptyNoteOnline') : t('world.emptyNote')}</p>
      <ol className="mt-8 grid w-full max-w-[720px] gap-4 border-t border-border pt-5 text-left sm:grid-cols-3">
        {steps.map((st, i) => (
          <li key={st.title}>
            <div className="text-[13px] font-semibold">
              <span className="mr-1.5 text-success-strong">{i + 1}.</span>
              {st.title}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">{st.hint}</p>
          </li>
        ))}
      </ol>
      <WorldTools server={s} phone={phone} className="mt-6" />
    </div>
  )
}

function leftoverTitle(c: WorldCopy): string {
  switch (c.kind) {
    case 'previous':
      return t('world.leftoverPrevious')
    case 'failed_restore':
      return t('world.leftoverFailed')
    default: {
      const unreachable: never = c.kind
      return unreachable
    }
  }
}

/** The newest world folder a restore left next to the live one, until it's discarded. */
function LeftoverCopy({ server: s, className }: { server: ServerStatus; className?: string }) {
  const ws = useWorkspace()
  const copies = usePoll(() => get<WorldCopy[]>(serverApi(s.id, '/world-copies')), 30_000, s.id)
  const newest = useListPresence(copies.data?.slice(0, 1), (c) => c.name)
  if (ws.stale) return null
  return (
    <>
      {newest.map(({ key, item, state }) => (
        <LeftoverNotice key={key} server={s} copy={item} state={state} onDiscarded={copies.refresh} className={className} />
      ))}
    </>
  )
}

function LeftoverNotice({ server: s, copy: c, state, onDiscarded, className }: { server: ServerStatus; copy: WorldCopy; state: Presence; onDiscarded: () => Promise<void>; className?: string }) {
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const when = formatDay(c.createdAt)

  async function discard() {
    setBusy(true)
    try {
      await del(serverApi(s.id, `/world-copies/${encodeURIComponent(c.name)}`))
      setConfirm(false)
      await onDiscarded()
    } catch (e) {
      toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div {...presenceProps(state)} className={className}>
      <Notice
        title={leftoverTitle(c)}
        action={
          <Button variant="outline" size="sm" disabledReason={busyReason(s)} onClick={() => setConfirm(true)}>
            <Trash2Icon />
            {t('world.leftoverDiscard')}
          </Button>
        }
      >
        {t('world.leftoverBody', { time: when, size: formatBytes(c.sizeBytes) })}
      </Notice>
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogPopup className="sm:max-w-[460px]">
          <DialogHeader>
            <DialogTitle className="text-lg font-bold">{t('world.leftoverDiscardTitle')}</DialogTitle>
            <DialogDescription>{t('world.leftoverDiscardBody', { time: when })}</DialogDescription>
          </DialogHeader>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button variant="ghost" onClick={() => setConfirm(false)}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" onClick={discard} loading={busy}>
              <Trash2Icon />
              {t('world.leftoverDiscardConfirm')}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </div>
  )
}
