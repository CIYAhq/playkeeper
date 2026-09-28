import { useEffect, useId, useRef, useState, type FormEvent, type MouseEvent, type ReactNode } from 'react'
import { ChevronRightIcon, CornerLeftUpIcon, DownloadIcon, FolderIcon, FolderInputIcon, PencilLineIcon, Trash2Icon } from 'lucide-react'
import { ApiError } from '@/api/client'
import { checkDownload, deleteFiles, downloadHref, listFiles, moveFiles } from '@/api/files'
import type { FileEntry, Files, ServerStatus } from '@/api/types'
import { errorText } from '@/api/workspace'
import { ListSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Sheet, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { baseName, crumbs, inside, inWorld, joinPath, nameProblem, parentOf, sortEntries } from '@/lib/files'
import { formatBytes, formatList, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { EntryIcon } from './entry'

/** A folder's name in a sentence: its path, or the server's folder. */
export function folderName(path: string): string {
  return path || t('files.rootName')
}

/** A dialog that asks for one name: a new folder or file, or a new name. Errors show under the field. */
export function NameDialog({
  open,
  onOpenChange,
  title,
  hint,
  initial = '',
  placeholder,
  submit,
  taken,
  selectBase,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  hint?: string
  initial?: string
  placeholder?: string
  submit: string
  taken: string[]
  /** Select the name without its extension, as for a rename. */
  selectBase?: boolean
  onSubmit: (name: string) => Promise<void>
}) {
  const [name, setName] = useState(initial)
  const [problem, setProblem] = useState<string>()
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!open) return
    setName(initial)
    setProblem(undefined)
    const id = window.requestAnimationFrame(() => {
      const el = input.current
      if (!el) return
      el.focus()
      const dot = initial.lastIndexOf('.')
      el.setSelectionRange(0, selectBase && dot > 0 ? dot : initial.length)
    })
    return () => window.cancelAnimationFrame(id)
  }, [open, initial, selectBase])
  const own = taken.filter((n) => n !== initial)
  async function go(e: FormEvent) {
    e.preventDefault()
    const bad = nameProblem(name, own)
    if (bad) {
      setProblem(t(bad))
      return
    }
    if (name === initial) {
      onOpenChange(false)
      return
    }
    setBusy(true)
    try {
      await onSubmit(name)
      onOpenChange(false)
    } catch (err) {
      setProblem(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  const errId = 'files-name-problem'
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[440px]">
        <form onSubmit={go} className="contents" noValidate>
          <div className="px-6 pt-6">
            <DialogTitle className="text-lg font-bold">{title}</DialogTitle>
            {hint && <DialogDescription className="mt-1 truncate text-[13px]">{hint}</DialogDescription>}
          </div>
          <DialogPanel className="flex flex-col gap-1.5 pt-4">
            <label className="text-[13px] font-medium" htmlFor="files-name">
              {t('files.nameLabel')}
            </label>
            <Input
              id="files-name"
              ref={input}
              value={name}
              placeholder={placeholder}
              onChange={(e) => {
                setName(e.target.value)
                setProblem(undefined)
              }}
              autoComplete="off"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              aria-invalid={problem ? true : undefined}
              aria-describedby={problem ? errId : undefined}
              className="max-sm:h-11"
            />
            {problem && (
              <p id={errId} className="text-xs text-destructive-foreground max-sm:text-[13px]" role="alert">
                {problem}
              </p>
            )}
          </DialogPanel>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button variant="ghost" onClick={() => onOpenChange(false)} className="max-sm:h-11">
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={busy} className="max-sm:h-11">
              {submit}
            </Button>
          </DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  )
}

/** Moves files and folders into a folder picked in the dialog. */
export function MoveDialog({ server, paths, open, onOpenChange, onMoved, worlds, running }: { server: ServerStatus; paths: string[]; open: boolean; onOpenChange: (open: boolean) => void; onMoved: (to: string) => void; worlds: string[]; running: boolean }) {
  const from = parentOf(paths[0] ?? '')
  const [at, setAt] = useState(from)
  const [list, setList] = useState<Files>()
  const [failed, setFailed] = useState<string>()
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (open) setAt(from)
  }, [open, from])
  useEffect(() => {
    if (!open) return
    let gone = false
    setList(undefined)
    setFailed(undefined)
    listFiles(server.id, at).then(
      (f) => !gone && setList(f),
      (e: unknown) => !gone && setFailed(errorText(e)),
    )
    return () => {
      gone = true
    }
  }, [open, server.id, at])
  const folders = sortEntries((list?.entries ?? []).filter((e) => e.type === 'folder'))
  const blocked = (p: string) => paths.some((m) => inside(p, m))
  const why =
    at === from
      ? t('files.move.same')
      : blocked(at)
        ? t('files.move.intoItself')
        : running && (inWorld(at, worlds) || paths.some((p) => inWorld(p, worlds)))
          ? t('files.reason.readOnly', { server: server.name })
          : undefined
  async function go() {
    setBusy(true)
    try {
      await moveFiles(
        server.id,
        paths.map((p) => ({ from: p, to: joinPath(at, baseName(p)) })),
      )
      toastManager.add({ title: paths.length === 1 ? t('files.movedOne', { name: baseName(paths[0] ?? '') }) : t('files.movedMany', { count: paths.length }), type: 'success' })
      onOpenChange(false)
      onMoved(at)
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      onMoved(at)
    } finally {
      setBusy(false)
    }
  }
  const row = 'flex min-h-11 w-full items-center gap-3 px-3 text-left text-sm disabled:cursor-not-allowed disabled:opacity-64 max-sm:min-h-14 max-sm:text-base'
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[480px]">
        <div className="px-6 pt-6">
          <DialogTitle className="text-lg font-bold">{paths.length === 1 ? t('files.move.titleOne', { name: baseName(paths[0] ?? '') }) : t('files.move.titleMany', { count: paths.length })}</DialogTitle>
          <DialogDescription className="mt-1 text-[13px]">{t('files.move.to')}</DialogDescription>
        </div>
        <DialogPanel className="flex flex-col gap-3 pt-4">
          <nav aria-label={t('files.breadcrumb')} className="flex min-w-0 flex-wrap items-center gap-1 text-[13px]">
            <CrumbButton current={at === ''} onClick={() => setAt('')}>
              {t('files.title')}
            </CrumbButton>
            {crumbs(at).map((c) => (
              <span key={c.path} className="flex min-w-0 items-center gap-1">
                <ChevronRightIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <CrumbButton current={c.path === at} onClick={() => setAt(c.path)}>
                  {c.name}
                </CrumbButton>
              </span>
            ))}
          </nav>
          <ul className="overflow-hidden rounded-2xl border border-border" aria-label={t('files.list', { folder: folderName(at) })}>
            {at !== '' && (
              <li className="border-b border-border last:border-b-0">
                <button type="button" className={row} onClick={() => setAt(parentOf(at))}>
                  <CornerLeftUpIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                  <span className="truncate">{t('files.move.up', { folder: folderName(parentOf(at)) })}</span>
                </button>
              </li>
            )}
            {!list && !failed && <ListSkeleton rows={3} lines={1} face="size-4 rounded" rowClassName="flex min-h-11 items-center gap-3 border-b border-border px-3 last:border-b-0" />}
            {failed && <li className="px-3 py-3 text-[13px] text-destructive-foreground">{failed}</li>}
            {list && folders.length === 0 && <li className="px-3 py-3 text-[13px] text-muted-foreground">{t('files.move.noFolders')}</li>}
            {folders.map((f) => {
              const p = joinPath(at, f.name)
              const no = blocked(p)
              return (
                <li key={f.name} className="border-b border-border last:border-b-0">
                  <button type="button" className={cn(row, 'hover:bg-accent/40')} disabled={no} title={no ? t('files.move.intoItself') : undefined} onClick={() => setAt(p)}>
                    <FolderIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                    <span className="min-w-0 flex-1 truncate">{f.name}</span>
                    <ChevronRightIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                  </button>
                </li>
              )
            })}
          </ul>
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)} className="max-sm:h-11">
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void go()} loading={busy} disabledReason={why} className="max-sm:h-11">
            <FolderInputIcon />
            {t('files.move.here')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

/** A folder on the way in the move dialog; the folder it shows now is only named. */
function CrumbButton({ current, onClick, children }: { current: boolean; onClick: () => void; children: ReactNode }) {
  if (current) {
    return (
      <span aria-current="location" className="max-w-40 truncate px-1 py-0.5 font-semibold text-foreground">
        {children}
      </span>
    )
  }
  return (
    <button type="button" onClick={onClick} className="max-w-40 truncate rounded-md px-1 py-0.5 text-muted-foreground outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring">
      {children}
    </button>
  )
}

/** Asks before deleting files and folders, which can't be brought back. */
export function DeleteDialog({ server, items, open, onOpenChange, onDeleted }: { server: ServerStatus; items: { path: string; type: FileEntry['type'] }[]; open: boolean; onOpenChange: (open: boolean) => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false)
  const names = items.map((i) => baseName(i.path))
  const folders = items.some((i) => i.type === 'folder')
  async function go() {
    setBusy(true)
    try {
      const { continuing } = await deleteFiles(
        server.id,
        items.map((i) => i.path),
      )
      if (continuing) toastManager.add({ title: items.length === 1 ? t('files.stillDeletingOne', { name: names[0] ?? '' }) : t('files.stillDeletingMany', { count: items.length }), description: t('files.stillDeletingBody') })
      else toastManager.add({ title: items.length === 1 ? t('files.deletedOne', { name: names[0] ?? '' }) : t('files.deletedMany', { count: items.length }), type: 'success' })
      onOpenChange(false)
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
      onDeleted()
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[440px]">
        <div className="px-6 pt-6 pb-2">
          <DialogTitle className="text-lg font-bold wrap-anywhere">{items.length === 1 ? t('files.delete.titleOne', { name: names[0] ?? '' }) : t('files.delete.titleMany', { count: items.length })}</DialogTitle>
          <DialogDescription className="mt-1 text-[13px]">{folders ? t('files.delete.folderBody') : t('files.delete.body')}</DialogDescription>
          {items.length > 1 && <p className="mt-2 line-clamp-3 text-[13px] wrap-anywhere text-muted-foreground">{formatList(names)}</p>}
        </div>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)} className="max-sm:h-11">
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={() => void go()} loading={busy} className="max-sm:h-11">
            <Trash2Icon />
            {t('files.delete.submit')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

/** Files dropped over files of the same names: replace them, or leave them out. */
export function ReplaceDialog({ names, open, onAnswer }: { names: string[]; open: boolean; onAnswer: (answer: 'replace' | 'skip' | 'cancel') => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onAnswer('cancel')}>
      <DialogPopup className="sm:max-w-[440px]">
        <div className="px-6 pt-6 pb-2">
          <DialogTitle className="text-lg font-bold wrap-anywhere">{names.length === 1 ? t('files.replace.titleOne', { name: names[0] ?? '' }) : t('files.replace.titleMany', { count: names.length })}</DialogTitle>
          <DialogDescription className="mt-1 text-[13px]">{t('files.replace.body', { count: names.length })}</DialogDescription>
          {names.length > 1 && <p className="mt-2 line-clamp-3 text-[13px] wrap-anywhere text-muted-foreground">{formatList(names)}</p>}
        </div>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={() => onAnswer('skip')} className="max-sm:h-11">
            {t('files.replace.skip')}
          </Button>
          <Button onClick={() => onAnswer('replace')} className="max-sm:h-11">
            {t('files.replace.replace')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

const infoText: Record<'binary' | 'link' | 'special', MessageKey> = { binary: 'files.info.binary', link: 'files.info.link', special: 'files.info.special' }

/** What a file that doesn't open in the editor is, and what can be done with it. */
export function InfoDialog({ server, entry, folder, open, onOpenChange, onDelete, canEdit }: { server: ServerStatus; entry: FileEntry | undefined; folder: string; open: boolean; onOpenChange: (open: boolean) => void; onDelete: () => void; canEdit: boolean }) {
  if (!entry) return null
  const kind = entry.type === 'link' ? 'link' : entry.type === 'special' ? 'special' : 'binary'
  const path = joinPath(folder, entry.name)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[440px]">
        <div className="flex items-start gap-3 px-6 pt-6 pb-2">
          <EntryIcon entry={entry} className="mt-0.5 size-5" />
          <div className="min-w-0">
            <DialogTitle className="text-lg font-bold wrap-anywhere">{entry.name}</DialogTitle>
            <DialogDescription className="mt-1 text-[13px]">{t(infoText[kind])}</DialogDescription>
            {entry.type === 'file' && <p className="mt-1 text-xs text-muted-foreground">{t('files.info.line', { size: formatBytes(entry.size), time: relativeTime(entry.modifiedAt) })}</p>}
          </div>
        </div>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          {canEdit && (
            <Button variant="destructive-outline" onClick={onDelete} className="max-sm:h-11 sm:mr-auto">
              <Trash2Icon />
              {t('files.delete')}
            </Button>
          )}
          <Button variant="ghost" onClick={() => onOpenChange(false)} className="max-sm:h-11">
            {t('common.close')}
          </Button>
          {entry.type === 'file' && (
            <Button className="max-sm:h-11" render={<a href={downloadHref(server.id, [path])} download={entry.name} onClick={() => onOpenChange(false)} />}>
              <DownloadIcon />
              {t('files.download')}
            </Button>
          )}
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

/** Asks before leaving what would be lost: unsaved changes, or uploads still running. */
export function LeaveDialog({ open, title, body, keep, leave, onKeep, onLeave }: { open: boolean; title: string; body: string; keep: string; leave: string; onKeep: () => void; onLeave: () => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onKeep()}>
      <DialogPopup className="sm:max-w-[420px]">
        <div className="px-6 pt-6 pb-2">
          <DialogTitle className="text-lg font-bold wrap-anywhere">{title}</DialogTitle>
          <DialogDescription className="mt-1 text-[13px]">{body}</DialogDescription>
        </div>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={onLeave} className="max-sm:h-11">
            {leave}
          </Button>
          <Button onClick={onKeep} className="max-sm:h-11">
            {keep}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

export interface RowAction {
  key: string
  label: string
  icon: ReactNode
  run?: () => void
  href?: string
  download?: string
  /** For a link: runs before it's followed, and may follow it later itself. */
  onLink?: (e: MouseEvent<HTMLAnchorElement>) => void
  danger?: boolean
  disabledReason?: string
}

/**
 * A zip's download link checks with the machine before it's followed, since a
 * link can't show why a download failed, such as a folder of too many files;
 * the download starts once the machine says it would. A click something else
 * answered already, as the live demo does, is left to it.
 */
export function checkedDownload(serverId: string, paths: string[]) {
  return (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.defaultPrevented) return
    e.preventDefault()
    const { href } = e.currentTarget
    const name = e.currentTarget.getAttribute('download') ?? ''
    checkDownload(serverId, paths).then(
      () => {
        const a = document.createElement('a')
        a.href = href
        a.download = name
        document.body.append(a)
        a.click()
        a.remove()
      },
      (err: unknown) => toastManager.add({ title: errorText(err), description: err instanceof ApiError ? err.hint : undefined, type: 'error' }),
    )
  }
}

/** The phone's actions for a file or folder, as a bottom sheet. */
export function ActionSheet({ title, actions, open, onOpenChange }: { title: string; actions: RowAction[]; open: boolean; onOpenChange: (open: boolean) => void }) {
  const id = useId()
  const row = 'flex min-h-14 w-full items-center gap-3.5 px-4 text-left text-base disabled:cursor-not-allowed disabled:opacity-64 [&_svg]:size-5'
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom" className="px-4">
        <div className="pt-3 pb-3">
          <SheetTitle className="truncate text-lg font-bold">{title}</SheetTitle>
        </div>
        <ul className="mb-2 overflow-hidden rounded-3xl border border-border bg-white">
          {actions.map((a) => (
            <li key={a.key} className="border-b border-border last:border-b-0">
              {a.href ? (
                <a
                  href={a.href}
                  download={a.download}
                  className={row}
                  onClick={(e) => {
                    a.onLink?.(e)
                    onOpenChange(false)
                  }}
                >
                  <span className="text-muted-foreground">{a.icon}</span>
                  {a.label}
                </a>
              ) : (
                <button
                  type="button"
                  className={cn(row, a.danger && 'text-destructive-foreground')}
                  disabled={!!a.disabledReason}
                  title={a.disabledReason}
                  aria-label={a.label}
                  aria-describedby={a.disabledReason ? `${id}-${a.key}` : undefined}
                  onClick={() => {
                    onOpenChange(false)
                    a.run?.()
                  }}
                >
                  <span className={a.danger ? 'text-destructive-foreground' : 'text-muted-foreground'}>{a.icon}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block">{a.label}</span>
                    {a.disabledReason && (
                      <span id={`${id}-${a.key}`} className="block truncate text-[13px] text-muted-foreground">
                        {a.disabledReason}
                      </span>
                    )}
                  </span>
                </button>
              )}
            </li>
          ))}
        </ul>
      </SheetPopup>
    </Sheet>
  )
}

export const actionIcons = {
  rename: <PencilLineIcon />,
  move: <FolderInputIcon />,
  delete: <Trash2Icon />,
  download: <DownloadIcon />,
}
