import { useState, type ReactNode } from 'react'
import { ArchiveIcon, ArrowRightIcon, ChevronDownIcon, ChevronRightIcon, ChevronUpIcon, CopyIcon, DownloadIcon, EllipsisIcon, HistoryIcon, MapIcon, PackageIcon, PencilIcon, RotateCcwIcon, ShieldCheckIcon, SlidersHorizontalIcon, Trash2Icon, UploadIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Backup, RestorePreview, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { EmptyArt, Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, copyText, SectionLabel } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatBytes, formatDate, formatDay, formatMs, relativeTime } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { CopyRestoreDialog, CopyRow, phoneStored, storedCell, storedRows, useCopyRestore, useStoredCopies } from './copy-restore'

const newestShown = 6

function downloadURL(s: ServerStatus, b: Backup): string {
  return serverApi(s.id, `/backups/${b.id}/download`)
}

export function WorldPage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const backups = usePoll(() => get<Backup[]>(serverApi(s.id, '/backups')), 10_000, s.id)
  const [preview, setPreview] = useState<RestorePreview>()
  const [restoreSheet, setRestoreSheet] = useState(false)
  const [showAll, setShowAll] = useState(false)
  const list = backups.data ?? []
  const refresh = () => void backups.refresh()
  const stored = useStoredCopies(s.id)
  const copies = stored.data?.copies ?? []
  const copiesOn = !!stored.data?.view.enabled
  const place = stored.data?.view.place ?? ''
  const rows = storedRows(list, copies, stored.data?.view)
  const shown = showAll ? rows : rows.slice(0, newestShown)
  const restore = useCopyRestore(s, setPreview)
  const jobDialog = <CopyRestoreDialog restore={restore} copies={copies} place={place} />
  const more =
    rows.length > newestShown ? (
      <button type="button" onClick={() => setShowAll(!showAll)} className="inline-flex min-h-8 items-center gap-1 self-start rounded-md text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring max-sm:px-4">
        {showAll ? t('world.showFewer') : t('world.showAll', { count: rows.length })}
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

  if (backups.data && rows.length === 0) {
    return (
      <>
        <EmptyBackups server={s} phone={phone} />
        {dialog}
        {jobDialog}
      </>
    )
  }

  if (phone) {
    const verified = list.length > 0 && list.every((b) => b.verified)
    return (
      <div className="flex flex-col gap-4">
        <MakeBackup server={s} phone onDone={refresh} />
        <section aria-labelledby="backups">
          <SectionLabel className="px-4">
            <span id="backups">{copiesOn ? t('world.phoneListCopies', { place }) : t('world.listPhone')}</span>
            {!copiesOn && verified && `${t('common.dot')}${t('world.allVerified')}`}
          </SectionLabel>
          <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {shown.map((r, i) =>
              r.kind === 'there' ? (
                <li key={r.copy.name} className="flex min-h-[70px] items-center gap-3 border-b border-border py-2 pr-3 pl-4 last:border-b-0">
                  <span className="min-w-0 flex-1">
                    <span className="block text-base">{formatDay(r.copy.createdAt)}</span>
                    <span className="block text-[13px] text-muted-foreground">{[formatBytes(r.copy.sizeBytes), phoneStored(r, place)].join(t('common.dot'))}</span>
                  </span>
                  <Button size="lg" variant="outline" onClick={() => void restore.start(r.copy)}>
                    <RotateCcwIcon />
                    {t('world.restoreCopyShort')}
                  </Button>
                </li>
              ) : (
                <li key={r.backup.id} className="flex min-h-[70px] items-center gap-3 border-b border-border py-2 pr-3 pl-4 last:border-b-0">
                  <span className="min-w-0 flex-1">
                    <span className="block text-base">{formatDay(r.backup.createdAt)}</span>
                    <span className="block text-[13px] text-muted-foreground">{[r.backup.note, formatBytes(r.backup.sizeBytes), phoneStored(r, place) ?? (r.backup.downloadedAt ? t('world.phoneDownloaded') : t('world.phoneNotDownloaded'))].filter(Boolean).join(t('common.dot'))}</span>
                  </span>
                  <Button size="lg" variant={i === 0 && !r.backup.downloadedAt && !copiesOn ? 'default' : 'outline'} render={<a href={downloadURL(s, r.backup)} download={r.backup.fileName} onClick={() => window.setTimeout(refresh, 3000)} />}>
                    <DownloadIcon />
                    {t('common.download')}
                  </Button>
                </li>
              ),
            )}
          </ul>
          {more && <div className="mt-2 flex">{more}</div>}
        </section>
        <div className="overflow-hidden rounded-3xl border border-border bg-white">
          <a {...linkProps({ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-rules' })} className="flex min-h-14 items-center gap-3 border-b border-border px-4">
            <SlidersHorizontalIcon className="size-5 text-muted-foreground" aria-hidden="true" />
            <span className="min-w-0 flex-1 text-base">{t('world.rules')}</span>
            <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
          </a>
          <button type="button" onClick={() => setRestoreSheet(true)} className={cn('flex w-full items-center gap-3 px-4 text-left', copiesOn ? 'min-h-14' : 'min-h-16')}>
            <RotateCcwIcon className="size-5 text-muted-foreground" aria-hidden="true" />
            <span className="min-w-0 flex-1">
              <span className={cn('block text-base', !copiesOn && 'font-medium')}>{t('world.restorePhone')}</span>
              {!copiesOn && <span className="block text-[13px] text-muted-foreground">{t('world.restorePhoneHint')}</span>}
            </span>
            <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
          </button>
        </div>
        {!copiesOn && <p className="px-1 pt-2 text-[13px] text-muted-foreground">{t('world.footnote')}</p>}
        <Sheet open={restoreSheet} onOpenChange={setRestoreSheet}>
          <SheetPopup side="bottom">
            <div className="px-5 pt-3">
              <SheetTitle className="text-lg font-bold">{t('world.restore')}</SheetTitle>
            </div>
            <SheetPanel className="flex flex-col gap-3 px-5 pt-4">
              <ul className="overflow-hidden rounded-2xl border border-border">
                {rows.map((r) => {
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
      <div className="grid gap-4 lg:grid-cols-[1.25fr_1fr]">
        <MakeBackup server={s} onDone={refresh} />
        <WorldInfo server={s} backups={list} />
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
              {shown.map((r, i) =>
                r.kind === 'there' ? (
                  <CopyRow key={r.copy.name} row={r} place={place} onRestore={() => void restore.start(r.copy)} />
                ) : (
                  <BackupRow key={r.backup.id} server={s} backup={r.backup} newest={i === 0} copiesOn={copiesOn} stored={storedCell(r, place)} onRestore={() => void restoreFrom(r.backup)} onChanged={refresh} />
                ),
              )}
            </tbody>
          </table>
        </div>
        {more && <div className="mt-2 flex">{more}</div>}
      </section>
      <Card className="mt-2">
        <CardTitle>{t('world.restore')}</CardTitle>
        <CardHint>{t('world.restoreHint')}</CardHint>
        <div className="mt-4 grid items-center gap-5 md:grid-cols-[1.6fr_1fr]">
          <RestoreDropZone server={s} onPreview={setPreview} />
          <ul className="flex flex-col gap-3 text-[13px] text-muted-foreground">
            <li>{t('world.restoreNote1')}</li>
            <li>{t('world.restoreNote2')}</li>
            <li>{t('world.restoreNote3', { server: s.name })}</li>
          </ul>
        </div>
      </Card>
      {ws.stale ? null : dialog}
      {ws.stale ? null : jobDialog}
    </>
  )
}

function MakeBackup({ server: s, phone, onDone }: { server: ServerStatus; phone?: boolean; onDone: () => void }) {
  const ws = useWorkspace()
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const running = s.operation?.kind === 'backup'
  const online = s.phase === 'online'
  const disabled = ws.stale || !s.exists || (!!s.operation && !running) || s.phase === 'docker_unavailable'

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
    <Button size={size} onClick={backup} loading={busy || running} disabled={disabled || running} className={size === 'touch' ? 'w-full' : undefined}>
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
            <p className="mt-1 text-[15px] leading-5 text-muted-foreground">{online ? t('world.makePhone', { server: s.name }) : t('world.makeBodyStopped', { server: s.name })}</p>
          </div>
        </div>
        <div className="mt-4">{button('touch')}</div>
      </Card>
    )
  }
  return (
    <Card>
      <CardTitle>{t('world.make')}</CardTitle>
      <CardHint>{t('world.makeHint')}</CardHint>
      <div className="mt-4 flex items-start gap-4">
        <Pip pose="letter" size={52} />
        <div className="min-w-0 text-[13px] leading-[18px]">
          <p>{online ? t('world.makeBody') : t('world.makeBodyStopped', { server: s.name })}</p>
          {online && <p className="mt-2 text-muted-foreground">{t('world.makeWarn')}</p>}
        </div>
      </div>
      <div className="mt-auto flex gap-2 pt-5">
        <InputGroup className="flex-1">
          <InputGroupAddon>
            <PencilIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('world.notePlaceholder')} aria-label={t('world.noteLabel')} maxLength={120} disabled={disabled || running} />
        </InputGroup>
        {button('default')}
      </div>
    </Card>
  )
}

function WorldInfo({ server: s, backups }: { server: ServerStatus; backups: Backup[] }) {
  const later = [
    { icon: <MapIcon />, title: t('world.pregen'), hint: t('world.pregenHint') },
    { icon: <PackageIcon />, title: t('world.packs'), hint: t('world.packsHint') },
    { icon: <UploadIcon />, title: t('world.ownWorld'), hint: t('world.ownWorldHint') },
  ]
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
          <dd className="mt-0.5 text-lg font-bold tabular-nums">{backups.length}</dd>
        </div>
      </dl>
      <ul className="mt-1 flex flex-col">
        {later.map((l) => (
          <li key={l.title} className="flex items-center gap-3 py-2.5 [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground">
            {l.icon}
            <span className="min-w-0 flex-1">
              <span className="block text-[13px] font-medium">{l.title}</span>
              <span className="block text-xs text-muted-foreground">{l.hint}</span>
            </span>
            <span className="text-xs text-muted-foreground">{t('common.later')}</span>
          </li>
        ))}
      </ul>
    </Card>
  )
}

function BackupRow({ server: s, backup: b, newest, copiesOn, stored, onRestore, onChanged }: { server: ServerStatus; backup: Backup; newest: boolean; copiesOn?: boolean; stored?: ReactNode; onRestore: () => void; onChanged: () => void }) {
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
  const detail = [kind, b.downtimeMs > 0 ? t('world.offline', { time: formatMs(b.downtimeMs) }) : undefined, t('unit.files', { count: b.fileCount })].filter(Boolean).join(t('common.dot'))
  const when = formatDay(b.createdAt)
  return (
    <tr className="h-12 border-t border-border">
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
  const players = s.phase === 'online' ? (s.players?.online ?? 0) : 0
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
      <Button
        size={phone ? 'touch' : 'lg'}
        className="mt-5 max-sm:w-full"
        loading={busy || running}
        disabled={ws.stale || !s.exists || (!!s.operation && !running) || running}
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
      <p className="mt-3 text-xs text-muted-foreground">{t('world.emptyNote', { count: players })}</p>
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
    </div>
  )
}
