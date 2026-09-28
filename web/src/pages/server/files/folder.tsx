import { useEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { ArrowDownIcon, ArrowUpIcon, ChevronDownIcon, ChevronLeftIcon, ChevronRightIcon, EllipsisIcon, FilePlusIcon, FolderOpenIcon, FolderPlusIcon, FolderUpIcon, ListChecksIcon, RotateCwIcon, SearchIcon, SquareIcon, UploadIcon, XIcon } from 'lucide-react'
import { downloadHref, listFiles, makeFolder, moveFiles, saveFile } from '@/api/files'
import type { FileEntry, ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Marker, Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { serverAction } from '@/components/app/server-action'
import { ListSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { StickyHeader } from '@/components/app/sticky-header'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuLinkItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { baseName, crumbs, droppedFiles, inWorld, joinPath, nameProblem, opensAsText, parentOf, pickedFiles, sortEntries, topNames, type Picked, type SortKey } from '@/lib/files'
import { formatBytes, relativeTime } from '@/lib/format'
import { busyReason } from '@/lib/phase'
import { linkProps, navigate, type Route } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { ActionSheet, actionIcons, checkedDownload, DeleteDialog, folderName, InfoDialog, MoveDialog, NameDialog, ReplaceDialog, type RowAction } from './dialogs'
import { EntryIcon } from './entry'
import { UploadPanel } from './uploads'
import type { FileUploads } from './uploads-state'

export const folderRoute = (s: ServerStatus, path: string): Route => ({ name: 'server', slug: s.slug, tab: 'files', ...(path ? { path } : {}) })
export const fileRoute = (s: ServerStatus, path: string): Route => ({ name: 'server', slug: s.slug, tab: 'files', path, file: true })

/** The Files tab's folder: its files and folders, what can be done with them, and uploads into it. */
export function FolderView({ server, path, uploads, bump, changed, onChanged }: { server: ServerStatus; path: string; uploads: FileUploads; bump: number; changed: boolean; onChanged: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const canEdit = can(ws.me, 'files.edit')
  const list = usePoll(() => listFiles(server.id, path), 15_000, `${server.id}:${path}`)
  const refresh = list.refresh
  useEffect(() => {
    if (bump > 0) void refresh()
  }, [bump, refresh])
  const files = list.data?.path === path ? list.data : undefined
  const running = files?.running ?? false
  const worlds = useMemo(() => files?.worlds ?? [], [files])
  const readOnly = running && inWorld(path, worlds)
  const busy = busyReason(server)
  const editReason = readOnly ? t('files.reason.readOnly', { server: server.name }) : busy
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: 'name', desc: false })
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [selecting, setSelecting] = useState(false)
  useEffect(() => {
    setFilter('')
    setSelected(new Set())
    setSelecting(false)
  }, [path])
  const entries = useMemo(() => {
    const q = filter.trim().toLowerCase()
    const shown = (files?.entries ?? []).filter((e) => !q || e.name.toLowerCase().includes(q))
    return sortEntries(shown, sort.key, sort.desc)
  }, [files, filter, sort])
  const selectedEntries = (files?.entries ?? []).filter((e) => selected.has(e.name))
  const selectedPaths = selectedEntries.map((e) => joinPath(path, e.name))

  const [dialog, setDialog] = useState<
    | { kind: 'folder' | 'file' }
    | { kind: 'rename'; entry: FileEntry }
    | { kind: 'move'; paths: string[] }
    | { kind: 'delete'; items: { path: string; type: FileEntry['type'] }[] }
    | { kind: 'info'; entry: FileEntry }
    | { kind: 'actions'; entry: FileEntry }
    | { kind: 'folderActions' }
    | { kind: 'replace'; picked: Picked[]; clash: string[] }
  >()
  const close = () => setDialog(undefined)
  const done = () => {
    setSelected(new Set())
    setSelecting(false)
    if (running) onChanged()
    void refresh()
  }

  const worldEntry = (e: FileEntry) => running && inWorld(joinPath(path, e.name), worlds)
  // Only the world folders say they're in use; inside one, the notice above says it for all.
  const worldFolder = (e: FileEntry) => running && worlds.includes(joinPath(path, e.name))
  const changeReason = (e?: FileEntry) => editReason ?? (e && worldEntry(e) ? t('files.reason.readOnly', { server: server.name }) : undefined)
  const selectionReason = editReason ?? (selectedEntries.some(worldEntry) ? t('files.reason.readOnly', { server: server.name }) : undefined)

  function open(e: FileEntry) {
    const p = joinPath(path, e.name)
    if (e.type === 'folder') navigate(folderRoute(server, p))
    else if (e.type === 'file' && opensAsText(e)) navigate(fileRoute(server, p))
    else setDialog({ kind: 'info', entry: e })
  }

  function actionsFor(e: FileEntry): RowAction[] {
    const p = joinPath(path, e.name)
    const reason = changeReason(e)
    const out: RowAction[] = []
    if (e.type === 'folder' || (e.type === 'file' && opensAsText(e))) out.push({ key: 'open', label: t('files.open'), icon: <FolderOpenIcon />, run: () => open(e) })
    if (e.type === 'file') out.push({ key: 'download', label: t('files.download'), icon: actionIcons.download, href: downloadHref(server.id, [p]), download: e.name })
    if (e.type === 'folder') out.push({ key: 'download', label: t('files.download'), icon: actionIcons.download, href: downloadHref(server.id, [p]), download: '', onLink: checkedDownload(server.id, [p]) })
    if (canEdit) {
      out.push({ key: 'rename', label: t('files.rename'), icon: actionIcons.rename, run: () => setDialog({ kind: 'rename', entry: e }), disabledReason: reason })
      out.push({ key: 'move', label: t('files.move'), icon: actionIcons.move, run: () => setDialog({ kind: 'move', paths: [p] }), disabledReason: reason })
      out.push({ key: 'delete', label: t('files.delete'), icon: actionIcons.delete, run: () => setDialog({ kind: 'delete', items: [{ path: p, type: e.type }] }), danger: true, disabledReason: reason })
    }
    return out
  }

  const filesInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  function start(picked: Picked[]) {
    if (!canEdit || !files || picked.length === 0) return
    if (editReason) {
      toastManager.add({ title: readOnly ? t('files.upload.readOnly', { server: server.name }) : editReason, type: 'error' })
      return
    }
    const bad = picked.find((p) => p.path.split('/').some((n) => nameProblem(n)))
    if (bad) {
      toastManager.add({ title: t('files.upload.badName', { name: bad.path }), type: 'error' })
      return
    }
    const here = new Set(files.entries.map((e) => e.name))
    const clash = topNames(picked).filter((n) => here.has(n))
    if (clash.length > 0) setDialog({ kind: 'replace', picked, clash })
    else uploads.add(path, picked, false)
  }
  function answer(a: 'replace' | 'skip' | 'cancel') {
    if (dialog?.kind !== 'replace') return
    const { picked, clash } = dialog
    close()
    if (a === 'replace') uploads.add(path, picked, true)
    else if (a === 'skip') uploads.add(path, picked.filter((p) => !clash.includes(p.path.split('/')[0] ?? p.path)), false)
  }
  const pickers = (
    <>
      <input
        ref={filesInput}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-label={t('files.chooseFiles')}
        onChange={(e) => {
          start(pickedFiles(e.target.files ?? []))
          e.target.value = ''
        }}
      />
      <input
        ref={folderInput}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-label={t('files.chooseFolder')}
        {...{ webkitdirectory: '' }}
        onChange={(e) => {
          start(pickedFiles(e.target.files ?? []))
          e.target.value = ''
        }}
      />
    </>
  )

  const [over, setOver] = useState(0)
  const drop = canEdit && !phone
    ? {
        onDragEnter: (e: DragEvent) => {
          if (!e.dataTransfer.types.includes('Files')) return
          e.preventDefault()
          setOver((n) => n + 1)
        },
        onDragOver: (e: DragEvent) => {
          if (e.dataTransfer.types.includes('Files')) e.preventDefault()
        },
        onDragLeave: () => setOver((n) => Math.max(0, n - 1)),
        onDrop: (e: DragEvent) => {
          e.preventDefault()
          setOver(0)
          void droppedFiles(e.dataTransfer).then(start)
        },
      }
    : {}

  const notice = readOnly ? (
    <Notice
      tone="warning"
      title={t('files.worldReadOnly', { server: server.name })}
      action={
        can(ws.me, 'servers.run') && (
          <Button variant="outline" size={phone ? 'default' : 'sm'} disabledReason={busy} onClick={() => void serverAction(server, 'stop')}>
            <SquareIcon />
            {t('files.stopServer', { server: server.name })}
          </Button>
        )
      }
    >
      {t('files.worldReadOnlyHint')}
    </Notice>
  ) : changed && running ? (
    <Notice
      title={t('files.changedRunning', { server: server.name })}
      action={
        can(ws.me, 'servers.run') && (
          <Button variant="outline" size={phone ? 'default' : 'sm'} disabledReason={busy} onClick={() => void serverAction(server, 'restart')}>
            <RotateCwIcon />
            {t('files.restartNow')}
          </Button>
        )
      }
    />
  ) : null

  const dialogs = (
    <>
      <NameDialog
        open={dialog?.kind === 'folder'}
        onOpenChange={(o) => !o && close()}
        title={t('files.newFolder')}
        hint={t('files.inFolder', { folder: folderName(path) })}
        submit={t('files.newFolder.create')}
        taken={(files?.entries ?? []).map((e) => e.name)}
        onSubmit={async (name) => {
          await makeFolder(server.id, joinPath(path, name))
          toastManager.add({ title: t('files.folderMade', { name }), type: 'success' })
          done()
        }}
      />
      <NameDialog
        open={dialog?.kind === 'file'}
        onOpenChange={(o) => !o && close()}
        title={t('files.newFile')}
        hint={t('files.inFolder', { folder: folderName(path) })}
        placeholder={t('files.newFile.placeholder')}
        submit={t('files.newFile.create')}
        taken={(files?.entries ?? []).map((e) => e.name)}
        onSubmit={async (name) => {
          await saveFile(server.id, joinPath(path, name), '', 'new')
          navigate(fileRoute(server, joinPath(path, name)))
        }}
      />
      <NameDialog
        open={dialog?.kind === 'rename'}
        onOpenChange={(o) => !o && close()}
        title={t('files.rename.title', { name: dialog?.kind === 'rename' ? dialog.entry.name : '' })}
        initial={dialog?.kind === 'rename' ? dialog.entry.name : ''}
        selectBase={dialog?.kind === 'rename' && dialog.entry.type !== 'folder'}
        submit={t('files.rename.submit')}
        taken={(files?.entries ?? []).map((e) => e.name)}
        onSubmit={async (name) => {
          if (dialog?.kind !== 'rename') return
          await moveFiles(server.id, [{ from: joinPath(path, dialog.entry.name), to: joinPath(path, name) }])
          toastManager.add({ title: t('files.renamed', { name }), type: 'success' })
          done()
        }}
      />
      {dialog?.kind === 'move' && <MoveDialog server={server} paths={dialog.paths} open onOpenChange={(o) => !o && close()} onMoved={done} worlds={worlds} running={running} />}
      {dialog?.kind === 'delete' && <DeleteDialog server={server} items={dialog.items} open onOpenChange={(o) => !o && close()} onDeleted={done} />}
      <InfoDialog
        server={server}
        entry={dialog?.kind === 'info' ? dialog.entry : undefined}
        folder={path}
        open={dialog?.kind === 'info'}
        onOpenChange={(o) => !o && close()}
        canEdit={canEdit && !(dialog?.kind === 'info' && changeReason(dialog.entry))}
        onDelete={() => dialog?.kind === 'info' && setDialog({ kind: 'delete', items: [{ path: joinPath(path, dialog.entry.name), type: dialog.entry.type }] })}
      />
      <ReplaceDialog names={dialog?.kind === 'replace' ? dialog.clash : []} open={dialog?.kind === 'replace'} onAnswer={answer} />
      {phone && (
        <ActionSheet title={dialog?.kind === 'actions' ? dialog.entry.name : ''} actions={dialog?.kind === 'actions' ? actionsFor(dialog.entry) : []} open={dialog?.kind === 'actions'} onOpenChange={(o) => !o && close()} />
      )}
      {phone && (
        <ActionSheet
          title={path ? baseName(path) : t('files.title')}
          open={dialog?.kind === 'folderActions'}
          onOpenChange={(o) => !o && close()}
          actions={[
            ...(canEdit
              ? [
                  { key: 'upload', label: t('files.uploadFiles'), icon: <UploadIcon />, run: () => filesInput.current?.click(), disabledReason: editReason },
                  { key: 'folder', label: t('files.newFolder'), icon: <FolderPlusIcon />, run: () => setDialog({ kind: 'folder' }), disabledReason: editReason },
                  { key: 'file', label: t('files.newFile'), icon: <FilePlusIcon />, run: () => setDialog({ kind: 'file' }), disabledReason: editReason },
                ]
              : []),
            { key: 'select', label: t('files.selectMode'), icon: <ListChecksIcon />, run: () => setSelecting(true), disabledReason: entries.length ? undefined : t('files.emptyReadOnly') },
          ]}
        />
      )}
    </>
  )

  const toggle = (name: string, on: boolean) =>
    setSelected((s) => {
      const next = new Set(s)
      if (on) next.add(name)
      else next.delete(name)
      return next
    })
  const allSelected = entries.length > 0 && entries.every((e) => selected.has(e.name))
  const someSelected = !allSelected && entries.some((e) => selected.has(e.name))

  const empty = files && files.entries.length === 0
  const noMatch = files && files.entries.length > 0 && entries.length === 0
  const emptyState = (
    <div className="flex flex-col items-center justify-center px-6 py-14 text-center">
      <Pip pose="box" size={88} />
      <p className="mt-3 text-base font-semibold">{canEdit && !editReason ? t('files.empty') : t('files.emptyReadOnly')}</p>
      {canEdit && !editReason && (
        <>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('files.emptyBody')}</p>
          <Button className="mt-4" onClick={() => filesInput.current?.click()} size={phone ? 'touch' : 'default'}>
            <UploadIcon />
            {t('files.uploadFiles')}
          </Button>
        </>
      )}
    </div>
  )
  const noMatchState = <p className="px-4 py-6 text-[13px] text-muted-foreground">{t('files.noMatch', { query: filter.trim() })}</p>
  const failed = list.error && !files && (
    <Notice tone="error" title={list.error.message} action={<Button variant="outline" onClick={() => void refresh()}>{t('common.tryAgain')}</Button>}>
      {list.error.hint}
    </Notice>
  )
  const moreNote = files?.more && <p className="px-4 py-3 text-xs text-muted-foreground max-sm:px-1 max-sm:text-[13px]">{t('files.more', { count: files.entries.length })}</p>

  if (phone) {
    const parent = path ? parentOf(path) : undefined
    return (
      <div className="flex flex-col gap-3 pb-24">
        <StickyHeader className="grid grid-cols-[1fr_auto_1fr] items-center gap-2 pt-2 pb-2">
          <a {...linkProps(parent === undefined ? { name: 'more' } : folderRoute(server, parent))} className="-ml-2 inline-flex min-h-11 min-w-0 items-center gap-0.5 justify-self-start rounded-lg px-1 text-[15px] font-medium text-success-strong">
            <ChevronLeftIcon className="size-5 shrink-0" aria-hidden="true" />
            <span className="truncate">{parent === undefined ? t('nav.more') : parent ? baseName(parent) : t('files.title')}</span>
          </a>
          <h1 className="max-w-[46vw] truncate text-[17px] font-semibold">{path ? baseName(path) : t('files.title')}</h1>
          <span className="justify-self-end">
            {selecting ? (
              <Button variant="ghost" className="h-11 px-2 text-[15px] font-medium text-success-strong" onClick={() => {
                setSelecting(false)
                setSelected(new Set())
              }}>
                {t('common.done')}
              </Button>
            ) : (
              <Button variant="ghost" size="icon-lg" aria-label={t('files.actions')} onClick={() => setDialog({ kind: 'folderActions' })}>
                <EllipsisIcon className="size-5" />
              </Button>
            )}
          </span>
        </StickyHeader>
        {notice && <div className="px-1">{notice}</div>}
        {failed}
        {files && files.entries.length > 12 && (
          <Input type="search" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t('files.filter')} aria-label={t('files.filter')} className="h-11 rounded-2xl bg-white text-base" />
        )}
        {!files && !list.error ? (
          <ListSkeleton rows={6} lines={2} face="size-5 rounded" className="overflow-hidden rounded-3xl border border-border bg-white" rowClassName="flex min-h-14 items-center gap-3 border-b border-border px-3 py-2 last:border-b-0" />
        ) : files && (
          <div className="overflow-hidden rounded-3xl border border-border bg-white">
            {empty ? emptyState : noMatch ? noMatchState : (
              <ul aria-label={t('files.list', { folder: folderName(path) })}>
                {entries.map((e) => (
                  <PhoneRow
                    key={e.name}
                    entry={e}
                    selecting={selecting}
                    selected={selected.has(e.name)}
                    inUse={worldFolder(e)}
                    onOpen={() => (selecting ? toggle(e.name, !selected.has(e.name)) : open(e))}
                    onActions={() => setDialog({ kind: 'actions', entry: e })}
                  />
                ))}
              </ul>
            )}
          </div>
        )}
        {moreNote}
        <UploadPanel uploads={uploads} phone />
        <div className="fixed inset-x-0 bottom-[calc(52px+env(safe-area-inset-bottom))] z-30 bg-gradient-to-t from-sidebar via-sidebar/95 to-sidebar/0 px-4 pt-4 pb-3">
          {selecting ? (
            <div className="flex items-center gap-2" role="group" aria-label={t('files.selected', { count: selected.size })}>
              <span className="min-w-0 flex-1 truncate text-[15px] font-semibold">{t('files.selected', { count: selected.size })}</span>
              <Button variant="outline" size="lg" className="h-11 bg-white" disabled={selected.size === 0} title={selected.size === 0 ? t('files.reason.selectInFolder') : undefined} render={selected.size ? <a href={downloadHref(server.id, selectedPaths)} download onClick={checkedDownload(server.id, selectedPaths)} /> : undefined}>
                {actionIcons.download}
                <span className="sr-only">{t('files.download')}</span>
              </Button>
              {canEdit && (
                <>
                  <Button variant="outline" size="lg" className="h-11 bg-white" disabledReason={selected.size === 0 ? t('files.reason.selectInFolder') : selectionReason} onClick={() => setDialog({ kind: 'move', paths: selectedPaths })} aria-label={t('files.move')}>
                    {actionIcons.move}
                  </Button>
                  <Button variant="destructive-outline" size="lg" className="h-11 bg-white" disabledReason={selected.size === 0 ? t('files.reason.selectInFolder') : selectionReason} onClick={() => setDialog({ kind: 'delete', items: selectedEntries.map((e) => ({ path: joinPath(path, e.name), type: e.type })) })} aria-label={t('files.delete')}>
                    {actionIcons.delete}
                  </Button>
                </>
              )}
            </div>
          ) : (
            canEdit && (
              <Button size="touch" className="w-full" disabledReason={editReason} onClick={() => filesInput.current?.click()}>
                <UploadIcon />
                {t('files.uploadFiles')}
              </Button>
            )
          )}
        </div>
        {pickers}
        {dialogs}
      </div>
    )
  }

  const sortButton = (key: SortKey, label: string, className?: string) => (
    <th scope="col" aria-sort={sort.key === key ? (sort.desc ? 'descending' : 'ascending') : 'none'} className={cn('px-2 font-medium', className)}>
      <button
        type="button"
        onClick={() => setSort((s) => ({ key, desc: s.key === key ? !s.desc : key !== 'name' }))}
        aria-label={sort.key !== key ? t('files.sortBy', { column: label }) : sort.desc ? t('files.sortAscending', { column: label }) : t('files.sortDescending', { column: label })}
        className={cn('inline-flex items-center gap-1 rounded-md py-1 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring', sort.key === key && 'text-foreground')}
      >
        {label}
        {sort.key === key && (sort.desc ? <ArrowDownIcon className="size-3.5" aria-hidden="true" /> : <ArrowUpIcon className="size-3.5" aria-hidden="true" />)}
      </button>
    </th>
  )

  return (
    <section className="relative flex flex-col gap-4" aria-labelledby="files-folder" {...drop}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Breadcrumb server={server} path={path} />
        <div className="ml-auto flex min-h-8 items-center gap-2">
          {selected.size > 0 ? (
            <>
              <span className="text-[13px] font-medium tabular-nums">{t('files.selected', { count: selected.size })}</span>
              <Button variant="outline" size="sm" render={<a href={downloadHref(server.id, selectedPaths)} download onClick={checkedDownload(server.id, selectedPaths)} />}>
                {actionIcons.download}
                {t('files.download')}
              </Button>
              {canEdit && (
                <>
                  <Button variant="outline" size="sm" disabledReason={selectionReason} onClick={() => setDialog({ kind: 'move', paths: selectedPaths })}>
                    {actionIcons.move}
                    {t('files.move')}
                  </Button>
                  <Button variant="destructive-outline" size="sm" disabledReason={selectionReason} onClick={() => setDialog({ kind: 'delete', items: selectedEntries.map((e) => ({ path: joinPath(path, e.name), type: e.type })) })}>
                    {actionIcons.delete}
                    {t('files.delete')}
                  </Button>
                </>
              )}
              <Button variant="ghost" size="icon-sm" aria-label={t('files.clearSelection')} onClick={() => setSelected(new Set())}>
                <XIcon />
              </Button>
            </>
          ) : (
            <>
              <label className="relative">
                <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 z-10 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
                <Input type="search" size="sm" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t('files.filter')} aria-label={t('files.filter')} className="w-52 [&_input]:pl-7" />
              </label>
              {canEdit && (
                <Menu>
                  <MenuTrigger render={<Button variant="outline" size="sm" disabled={!!editReason} title={editReason} className={editReason ? 'disabled:pointer-events-auto disabled:cursor-not-allowed' : undefined} />}>
                    <FolderPlusIcon />
                    {t('files.new')}
                    <ChevronDownIcon className="opacity-60" />
                  </MenuTrigger>
                  <MenuPopup align="end" className="min-w-48">
                    <MenuItem onClick={() => setDialog({ kind: 'folder' })}>
                      <FolderPlusIcon />
                      {t('files.newFolder')}
                    </MenuItem>
                    <MenuItem onClick={() => setDialog({ kind: 'file' })}>
                      <FilePlusIcon />
                      {t('files.newFile')}
                    </MenuItem>
                    <MenuSeparator />
                    <MenuItem onClick={() => folderInput.current?.click()}>
                      <FolderUpIcon />
                      {t('files.uploadFolder')}
                    </MenuItem>
                  </MenuPopup>
                </Menu>
              )}
              {canEdit && (
                <Button size="sm" disabledReason={editReason} onClick={() => filesInput.current?.click()}>
                  <UploadIcon />
                  {t('files.upload')}
                </Button>
              )}
            </>
          )}
        </div>
      </div>
      {notice}
      {failed}
      <UploadPanel uploads={uploads} phone={false} />
      {!failed && (
        <div className="relative overflow-hidden rounded-2xl border border-border bg-card shadow-card">
          <table className="w-full table-fixed text-sm" aria-label={t('files.list', { folder: folderName(path) })}>
            <thead className="border-b border-border bg-muted/50 text-left text-xs text-muted-foreground">
              <tr className="h-10">
                <th scope="col" className="w-11 pl-4">
                  <Checkbox checked={allSelected} indeterminate={someSelected} disabled={entries.length === 0} onCheckedChange={(on) => setSelected(on ? new Set(entries.map((e) => e.name)) : new Set())} aria-label={t('files.selectAll')} />
                </th>
                {sortButton('name', t('files.col.name'))}
                {sortButton('size', t('files.col.size'), 'w-28 text-right [&>button]:flex-row-reverse')}
                {sortButton('modified', t('files.col.modified'), 'w-40')}
                <th scope="col" className="w-12">
                  <span className="sr-only">{t('common.moreActions')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {!files && !list.error && <TableSkeleton rows={6} cols={['start', 'start', 'end', 'start', 'start']} rowClassName="h-12 border-b border-border last:border-b-0" />}
              {entries.map((e) => (
                <DesktopRow
                  key={e.name}
                  entry={e}
                  selected={selected.has(e.name)}
                  onSelect={(on) => toggle(e.name, on)}
                  onOpen={() => open(e)}
                  inUse={worldFolder(e)}
                  menu={<RowMenu name={e.name} actions={actionsFor(e)} />}
                />
              ))}
            </tbody>
          </table>
          {empty && emptyState}
          {noMatch && noMatchState}
          {moreNote}
          {over > 0 && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center rounded-2xl border-2 border-dashed border-primary bg-selected/90 animate-fade">
              <p className="flex items-center gap-2 text-sm font-semibold text-success-strong">
                <UploadIcon className="size-4" aria-hidden="true" />
                {t('files.drop', { folder: folderName(path) })}
              </p>
            </div>
          )}
        </div>
      )}
      {pickers}
      {dialogs}
    </section>
  )
}

function Breadcrumb({ server, path }: { server: ServerStatus; path: string }) {
  const all = crumbs(path)
  return (
    <nav aria-label={t('files.breadcrumb')} className="flex min-w-0 flex-wrap items-center gap-1 text-[15px]">
      <h2 id="files-folder" className="contents">
        {path ? (
          <a {...linkProps(folderRoute(server, ''))} className="rounded-md px-1 font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
            {t('files.title')}
          </a>
        ) : (
          <span className="px-1 font-bold" aria-current="page">
            {t('files.title')}
          </span>
        )}
        {all.map((c, i) => (
          <span key={c.path} className="flex min-w-0 items-center gap-1">
            {/* A space keeps the names apart in the heading's text, which names the folder's section. */}
            {' '}
            <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            {i === all.length - 1 ? (
              <span className="max-w-64 truncate px-1 font-bold" aria-current="page">
                {c.name}
              </span>
            ) : (
              <a {...linkProps(folderRoute(server, c.path))} className="max-w-48 truncate rounded-md px-1 font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
                {c.name}
              </a>
            )}
          </span>
        ))}
      </h2>
    </nav>
  )
}

function EntryMarker({ entry, inUse }: { entry: FileEntry; inUse: boolean }) {
  if (entry.type === 'link') return <Marker tone="amber">{t('files.marker.link')}</Marker>
  if (entry.type === 'special') return <Marker tone="amber">{t('files.marker.special')}</Marker>
  if (inUse && entry.type === 'folder') return <Marker>{t('files.marker.inUse')}</Marker>
  return null
}

function DesktopRow({ entry: e, selected, onSelect, onOpen, inUse, menu }: { entry: FileEntry; selected: boolean; onSelect: (on: boolean) => void; onOpen: () => void; inUse: boolean; menu: ReactNode }) {
  return (
    <tr className={cn('h-12 border-b border-border transition-colors duration-(--motion-fast) last:border-b-0 hover:bg-accent/40', selected && 'bg-selected hover:bg-selected')}>
      <td className="pl-4">
        <Checkbox checked={selected} onCheckedChange={(on) => onSelect(on === true)} aria-label={t('files.select', { name: e.name })} />
      </td>
      <td className="px-2">
        <button type="button" onClick={onOpen} className="-mx-1 flex max-w-full min-w-0 items-center gap-2.5 rounded-md px-1 py-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <EntryIcon entry={e} />
          <span className="truncate font-medium">{e.name}</span>
          <EntryMarker entry={e} inUse={inUse} />
        </button>
      </td>
      <td className="px-2 text-right text-[13px] text-muted-foreground tabular-nums">{e.type === 'file' ? formatBytes(e.size) : t('common.none')}</td>
      <td className="truncate px-2 text-[13px] text-muted-foreground" title={e.modifiedAt}>
        {relativeTime(e.modifiedAt)}
      </td>
      <td className="pr-3 text-right">{menu}</td>
    </tr>
  )
}

function RowMenu({ name, actions }: { name: string; actions: RowAction[] }) {
  const last = actions.findIndex((a) => a.danger)
  return (
    <Menu>
      <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('files.menuFor', { name })} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-44">
        {actions.map((a, i) => (
          <span key={a.key} className="contents">
            {i === last && i > 0 && <MenuSeparator />}
            {a.href ? (
              <MenuLinkItem href={a.href} download={a.download} onClick={a.onLink}>
                {a.icon}
                {a.label}
              </MenuLinkItem>
            ) : (
              <MenuItem variant={a.danger ? 'destructive' : 'default'} disabled={!!a.disabledReason} title={a.disabledReason} onClick={a.run}>
                {a.icon}
                {a.label}
              </MenuItem>
            )}
          </span>
        ))}
      </MenuPopup>
    </Menu>
  )
}

function PhoneRow({ entry: e, selecting, selected, inUse, onOpen, onActions }: { entry: FileEntry; selecting: boolean; selected: boolean; inUse: boolean; onOpen: () => void; onActions: () => void }) {
  const line = e.type === 'file' ? t('files.phoneLine', { size: formatBytes(e.size), time: relativeTime(e.modifiedAt) }) : relativeTime(e.modifiedAt)
  return (
    <li className={cn('flex items-center border-b border-border last:border-b-0', selected && 'bg-selected')}>
      <button type="button" onClick={onOpen} aria-pressed={selecting ? selected : undefined} className="flex min-h-14 min-w-0 flex-1 items-center gap-3 py-2 pl-3 text-left">
        {selecting && <Checkbox checked={selected} tabIndex={-1} aria-hidden="true" className="pointer-events-none" />}
        <EntryIcon entry={e} className="size-5" />
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate text-base leading-5">{e.name}</span>
            <EntryMarker entry={e} inUse={inUse} />
          </span>
          <span className="block truncate text-[13px] text-muted-foreground">{line}</span>
        </span>
        {!selecting && e.type === 'folder' && <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
      </button>
      {!selecting && (
        <Button variant="ghost" size="icon-lg" className="mr-1" aria-label={t('files.menuFor', { name: e.name })} onClick={onActions}>
          <EllipsisIcon className="size-5" />
        </Button>
      )}
    </li>
  )
}
