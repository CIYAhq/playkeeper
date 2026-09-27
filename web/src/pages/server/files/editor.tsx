import { lazy, Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronLeftIcon, ChevronRightIcon, CircleAlertIcon, DownloadIcon, RotateCwIcon, SaveIcon, SquareIcon } from 'lucide-react'
import { ApiError } from '@/api/client'
import { downloadHref, openFile, saveFile } from '@/api/files'
import type { FileContent, ServerStatus } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Notice } from '@/components/app/bits'
import type { CodeEditorHandle, EditorProblem } from '@/components/app/code-editor'
import { useIsPhone } from '@/components/app/controls'
import { serverAction } from '@/components/app/server-action'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { baseName, crumbs, isPlayerList, languageNames, languageOf, parentOf } from '@/lib/files'
import { formatBytes } from '@/lib/format'
import { busyReason } from '@/lib/phase'
import { linkProps } from '@/lib/router'
import { cn } from '@/lib/utils'
import { folderName } from './dialogs'
import { folderRoute } from './folder'

const CodeEditor = lazy(() => import('@/components/app/code-editor').then((m) => ({ default: m.CodeEditor })))

/** A file open in the Files tab: the editor for text, a way to download anything else. dirty reports unsaved changes to the tab, which asks before they're lost. */
export function FileView({ server, path, onDirty, onChanged }: { server: ServerStatus; path: string; onDirty: (dirty: boolean) => void; onChanged: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const canEdit = can(ws.me, 'files.edit')
  const [content, setContent] = useState<FileContent>()
  const [error, setError] = useState<ApiError>()
  // The text loaded into the editor, and the text it has now.
  const [loaded, setLoaded] = useState('')
  const text = useRef('')
  const saved = useRef('')
  const [dirty, setDirty] = useState(false)
  const [version, setVersion] = useState('')
  const [size, setSize] = useState(0)
  const [saving, setSaving] = useState(false)
  const [savedRunning, setSavedRunning] = useState(false)
  const [cursor, setCursor] = useState({ line: 1, column: 1 })
  const [problems, setProblems] = useState<EditorProblem[]>([])
  const [ask, setAsk] = useState<'invalid' | 'changed' | 'gone'>()
  const editor = useRef<CodeEditorHandle>(null)
  const name = baseName(path)
  const folder = parentOf(path)
  const language = languageOf(name)

  const load = useCallback(async () => {
    setError(undefined)
    try {
      const c = await openFile(server.id, path)
      setContent(c)
      setLoaded(c.text)
      text.current = c.text
      saved.current = c.text
      setVersion(c.version)
      setSize(c.size)
      setDirty(false)
    } catch (e) {
      setContent(undefined)
      setError(e instanceof ApiError ? e : new ApiError(0, { error: errorText(e), code: 'internal' }))
    }
  }, [server.id, path])
  useEffect(() => {
    setContent(undefined)
    setSavedRunning(false)
    void load()
  }, [load])
  useEffect(() => {
    onDirty(dirty)
  }, [dirty, onDirty])
  useEffect(() => () => onDirty(false), [onDirty])

  const running = content?.running ?? false
  const busy = busyReason(server)
  const saveReason = content?.readOnly ? t('files.reason.readOnly', { server: server.name }) : (busy ?? (!dirty ? t('files.editor.noChanges') : undefined))

  const save = useCallback(
    async (force = false, anyway = false) => {
      if (!canEdit || content?.readOnly || (!dirty && !force)) return
      if (problems.length > 0 && !anyway) {
        setAsk('invalid')
        return
      }
      setAsk(undefined)
      setSaving(true)
      const sent = text.current
      try {
        const info = await saveFile(server.id, path, sent, force ? '' : version)
        setVersion(info.version)
        setSize(info.size)
        saved.current = sent
        setDirty(text.current !== sent)
        toastManager.add({ title: t('files.editor.savedToast', { name }), type: 'success' })
        if (running) {
          setSavedRunning(true)
          onChanged()
        }
      } catch (e) {
        if (e instanceof ApiError && e.code === 'file_changed') setAsk(e.params?.gone ? 'gone' : 'changed')
        else toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })
      } finally {
        setSaving(false)
      }
    },
    [canEdit, content?.readOnly, dirty, problems.length, server.id, path, version, name, running, onChanged],
  )
  const saveRef = useRef(save)
  useEffect(() => {
    saveRef.current = save
  })
  // ⌘S / Ctrl+S saves wherever the focus is on the page.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void saveRef.current()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const back = folderRoute(server, folder)
  const download = downloadHref(server.id, [path])
  const header = phone ? (
    <header className="grid grid-cols-[1fr_auto_1fr] items-center gap-2 pt-2 pb-1">
      <a {...linkProps(back)} className="-ml-2 inline-flex min-h-11 min-w-0 items-center gap-0.5 justify-self-start rounded-lg px-1 text-[15px] font-medium text-success-strong">
        <ChevronLeftIcon className="size-5 shrink-0" aria-hidden="true" />
        <span className="truncate">{folder ? baseName(folder) : t('files.title')}</span>
      </a>
      <h1 className="max-w-[46vw] truncate text-[17px] font-semibold">{name}</h1>
      <span className="justify-self-end">
        {canEdit && content && !content.binary && (
          <Button variant="ghost" className="h-11 px-2 text-[15px] font-semibold text-success-strong" loading={saving} disabledReason={saveReason} onClick={() => void save()}>
            {t('common.save')}
          </Button>
        )}
      </span>
    </header>
  ) : (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <FileCrumbs server={server} path={path} />
      {content && !content.binary && (
        <div className="ml-auto flex min-h-8 items-center gap-2">
          <span className={cn('text-[13px]', dirty ? 'font-medium text-warning-foreground' : 'text-muted-foreground')} role="status">
            {dirty ? t('files.editor.unsaved') : content.readOnly || !canEdit ? t('files.editor.readOnly') : t('files.editor.saved')}
          </span>
          <Button variant="outline" size="sm" render={<a href={download} download={name} />}>
            <DownloadIcon />
            {t('files.download')}
          </Button>
          {canEdit && (
            <Button size="sm" loading={saving} disabledReason={saveReason} onClick={() => void save()}>
              <SaveIcon />
              {t('common.save')}
            </Button>
          )}
        </div>
      )}
    </div>
  )

  if (error || content?.binary) {
    const tooLarge = error?.code === 'too_large'
    const gone = error?.status === 404
    const title = content?.binary ? t('files.editor.binary') : tooLarge ? t('files.editor.tooLarge') : gone ? t('files.editor.gone') : t('files.editor.notOpened')
    const body = content?.binary ? t('files.editor.binaryBody') : tooLarge ? t('files.editor.tooLargeBody') : gone ? t('files.editor.goneBody') : (error?.message ?? '')
    return (
      <div className={cn('flex flex-col gap-4', phone && 'pb-6')}>
        {header}
        <div className="flex flex-col items-center justify-center rounded-3xl border border-border bg-card px-6 py-14 text-center shadow-card">
          <Pip pose={gone ? 'search' : 'box'} size={88} />
          <h2 className="mt-3 text-lg font-bold wrap-anywhere">{title}</h2>
          <p className="mt-1 max-w-[420px] text-[13px] text-muted-foreground">{body}</p>
          <div className="mt-5 flex flex-wrap justify-center gap-2">
            <Button variant="outline" size={phone ? 'lg' : 'default'} render={<a {...linkProps(back)} />}>
              {t('files.editor.backTo', { folder: folder ? baseName(folder) : t('files.title') })}
            </Button>
            {(content?.binary || tooLarge) && (
              <Button size={phone ? 'lg' : 'default'} render={<a href={download} download={name} />}>
                <DownloadIcon />
                {t('files.download')}
              </Button>
            )}
            {error && !tooLarge && !gone && (
              <Button size={phone ? 'lg' : 'default'} onClick={() => void load()}>
                {t('common.tryAgain')}
              </Button>
            )}
          </div>
        </div>
      </div>
    )
  }

  const managed = content?.managed ?? []
  const small = phone ? 'default' : 'sm'
  const notice: ReactNode = !content ? null : content.readOnly ? (
    <Notice
      tone="warning"
      title={t('files.worldReadOnly', { server: server.name })}
      action={
        can(ws.me, 'servers.run') && (
          <Button variant="outline" size={small} disabledReason={busy} onClick={() => void serverAction(server, 'stop')}>
            <SquareIcon />
            {t('files.stopServer', { server: server.name })}
          </Button>
        )
      }
    >
      {t('files.worldReadOnlyHint')}
    </Notice>
  ) : savedRunning && running ? (
    <Notice
      title={t('files.editor.savedRestart', { server: server.name })}
      action={
        can(ws.me, 'servers.run') && (
          <Button variant="outline" size={small} disabledReason={busy} onClick={() => void serverAction(server, 'restart')}>
            <RotateCwIcon />
            {t('files.restartNow')}
          </Button>
        )
      }
    />
  ) : managed.length > 0 ? (
    <Notice
      title={t('files.editor.managed', { server: server.name })}
      action={
        can(ws.me, 'servers.manage') && (
          <Button variant="outline" size={small} render={<a {...linkProps({ name: 'server', slug: server.slug, tab: 'settings' })} />}>
            {t('files.editor.openSettings')}
          </Button>
        )
      }
    />
  ) : isPlayerList(path) && running ? (
    <Notice
      title={t('files.editor.playerList', { server: server.name })}
      action={
        <Button variant="outline" size={small} render={<a {...linkProps({ name: 'server', slug: server.slug, tab: 'players' })} />}>
          {t('files.editor.openPlayers')}
        </Button>
      }
    />
  ) : running ? (
    <p className="text-[13px] text-muted-foreground" role="status">
      {t('files.editor.running', { server: server.name })}
    </p>
  ) : null

  const box = phone ? 'h-[calc(100dvh-230px)] min-h-[300px]' : 'h-[calc(100dvh-290px)] min-h-[380px]'
  return (
    <div className="flex flex-col gap-3">
      {header}
      {notice && <div className={cn(phone && 'px-1')}>{notice}</div>}
      {!content ? (
        <>
          <LoadingLabel />
          <Skeleton className={cn('w-full rounded-2xl', box)} />
        </>
      ) : (
        <div className={cn('flex min-h-0 flex-col overflow-hidden rounded-2xl border border-border bg-card shadow-card max-sm:rounded-3xl', box)}>
          <div className="min-h-0 flex-1">
            <Suspense fallback={<Skeleton className="h-full w-full rounded-none" />}>
              <CodeEditor
                ref={editor}
                value={loaded}
                language={language}
                readOnly={!canEdit || !!content.readOnly}
                label={t('files.editor.label', { name })}
                phone={phone}
                managed={managed}
                onChange={(v) => {
                  text.current = v
                  setDirty(v !== saved.current)
                }}
                onSave={() => void save()}
                onCursor={(line, column) => setCursor({ line, column })}
                onProblems={setProblems}
              />
            </Suspense>
          </div>
          <div className="flex h-8 shrink-0 items-center gap-2 border-t border-border bg-warm px-3 text-xs text-muted-foreground tabular-nums max-sm:h-9 max-sm:text-[13px]">
            <span className="truncate">{t('files.editor.position', cursor)}</span>
            <span aria-hidden="true">{t('common.dot').trim()}</span>
            <span className="truncate">{t(languageNames[language])}</span>
            <span aria-hidden="true" className="max-sm:hidden">
              {t('common.dot').trim()}
            </span>
            <span className="max-sm:hidden">{formatBytes(size)}</span>
            {problems.length > 0 && (
              <button type="button" className="ml-auto inline-flex min-w-0 items-center gap-1 rounded-md px-1 font-medium text-destructive-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring" onClick={() => editor.current?.goToLine(problems[0]?.line ?? 1)}>
                <CircleAlertIcon className="size-3.5 shrink-0" aria-hidden="true" />
                <span className="truncate">{t('files.editor.problems', { count: problems.length })}</span>
              </button>
            )}
          </div>
        </div>
      )}
      <Dialog open={ask === 'invalid'} onOpenChange={(o) => !o && setAsk(undefined)}>
        <DialogPopup className="sm:max-w-[440px]">
          <div className="px-6 pt-6 pb-2">
            <DialogTitle className="text-lg font-bold wrap-anywhere">{t('files.invalid.title', { name, line: problems[0]?.line ?? 1 })}</DialogTitle>
            <DialogDescription className="mt-1 text-[13px]">{problems[0]?.message ?? ''}</DialogDescription>
            <p className="mt-1 text-[13px] text-muted-foreground">{t('files.invalid.body')}</p>
          </div>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button variant="ghost" className="max-sm:h-11" onClick={() => void save(false, true)}>
              {t('files.invalid.save')}
            </Button>
            <Button
              className="max-sm:h-11"
              onClick={() => {
                setAsk(undefined)
                editor.current?.goToLine(problems[0]?.line ?? 1)
              }}
            >
              {t('files.invalid.keep')}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
      <Dialog open={ask === 'changed' || ask === 'gone'} onOpenChange={(o) => !o && setAsk(undefined)}>
        <DialogPopup className="sm:max-w-[460px]">
          <div className="px-6 pt-6 pb-2">
            <DialogTitle className="text-lg font-bold wrap-anywhere">{ask === 'gone' ? t('files.conflict.goneTitle', { name }) : t('files.conflict.title', { name })}</DialogTitle>
            <DialogDescription className="mt-1 text-[13px]">{ask === 'gone' ? t('files.conflict.goneBody') : t('files.conflict.body')}</DialogDescription>
          </div>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            {ask === 'changed' && (
              <Button
                variant="ghost"
                className="max-sm:h-11"
                onClick={() => {
                  setAsk(undefined)
                  void load()
                }}
              >
                {t('files.conflict.theirs')}
              </Button>
            )}
            <Button className="max-sm:h-11" loading={saving} onClick={() => void save(true, true)}>
              {t('files.conflict.mine')}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </div>
  )
}

function FileCrumbs({ server, path }: { server: ServerStatus; path: string }) {
  const all = crumbs(path)
  return (
    <nav aria-label={t('files.breadcrumb')} className="flex min-w-0 flex-wrap items-center gap-1 text-[15px]">
      <a {...linkProps(folderRoute(server, ''))} className="rounded-md px-1 font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
        {t('files.title')}
      </a>
      {all.map((c, i) => (
        <span key={c.path} className="flex min-w-0 items-center gap-1">
          <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          {i === all.length - 1 ? (
            <h2 className="max-w-72 truncate px-1 font-bold" aria-current="page">
              {c.name}
            </h2>
          ) : (
            <a {...linkProps(folderRoute(server, c.path))} className="max-w-48 truncate rounded-md px-1 font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring" title={folderName(c.path)}>
              {c.name}
            </a>
          )}
        </span>
      ))}
    </nav>
  )
}
