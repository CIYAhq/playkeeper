import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import type { ServerStatus } from '@/api/types'
import { LoadingLabel } from '@/components/app/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { baseName } from '@/lib/files'
import { guardLeaving, navigate } from '@/lib/router'
import { LeaveDialog } from './dialogs'
import { FolderView } from './folder'
import { useFileUploads } from './uploads-state'

// The editor's page loads with CodeMirror the first time a file opens.
const FileView = lazy(() => import('./editor').then((m) => ({ default: m.FileView })))

/**
 * A server's Files tab: its folders and the files open in the editor. The
 * tab keeps its uploads going while you move between them, and asks before
 * unsaved changes or running uploads would be lost.
 */
export function FilesPage({ server, path, file }: { server: ServerStatus; path: string; file: boolean }) {
  const [bump, setBump] = useState(0)
  // A change made while the game ran applies when it restarts; the tab says so until it does.
  const [changed, setChanged] = useState(false)
  const online = server.phase === 'online'
  useEffect(() => {
    if (!online) setChanged(false)
  }, [online])
  const onPlaced = useCallback(() => {
    setBump((n) => n + 1)
    if (online) setChanged(true)
  }, [online])
  const uploads = useFileUploads(server.id, onPlaced)
  const onChanged = useCallback(() => setChanged(true), [])

  const [dirty, setDirty] = useState(false)
  const [leaving, setLeaving] = useState<{ to: string; why: 'dirty' | 'uploads' }>()
  const letGo = useRef(false)
  const here = `/servers/${server.slug}/`
  const inTab = useCallback((to: string) => to.startsWith(`${here}files`) || to.startsWith(`${here}file/`), [here])
  const editing = `${here}file/${path.split('/').map(encodeURIComponent).join('/')}`
  const busy = uploads.busy
  useEffect(() => {
    if (!dirty && !busy) return
    return guardLeaving((to) => {
      if (letGo.current) return true
      const target = to.split('#')[0] ?? to
      if (dirty && target !== editing) {
        setLeaving({ to, why: 'dirty' })
        return false
      }
      if (busy && !inTab(target)) {
        setLeaving({ to, why: 'uploads' })
        return false
      }
      return true
    })
  }, [dirty, busy, editing, inTab])
  useEffect(() => {
    if (!dirty && !busy) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty, busy])

  function leave() {
    const l = leaving
    if (!l) return
    setLeaving(undefined)
    if (l.why === 'uploads' || (busy && !inTab(l.to))) uploads.stopAll()
    setDirty(false)
    letGo.current = true
    navigate(l.to)
    letGo.current = false
  }

  return (
    <>
      {file ? (
        <Suspense
          fallback={
            <div className="flex flex-col gap-3">
              <LoadingLabel />
              <Skeleton className="h-8 w-72" />
              <Skeleton className="h-[calc(100dvh-290px)] min-h-[380px] w-full rounded-2xl max-sm:h-[calc(100dvh-230px)] max-sm:min-h-[300px]" />
            </div>
          }
        >
          <FileView key={path} server={server} path={path} onDirty={setDirty} onChanged={onChanged} />
        </Suspense>
      ) : (
        <FolderView server={server} path={path} uploads={uploads} bump={bump} changed={changed} onChanged={onChanged} />
      )}
      <LeaveDialog
        open={!!leaving}
        title={leaving?.why === 'dirty' ? t('files.leave.title', { name: baseName(path) }) : t('files.leave.uploadsTitle')}
        body={leaving?.why === 'dirty' ? t('files.leave.body') : t('files.leave.uploadsBody')}
        keep={leaving?.why === 'dirty' ? t('files.leave.keep') : t('files.leave.keepUploading')}
        leave={leaving?.why === 'dirty' ? t('files.leave.discard') : t('files.leave.stop')}
        onKeep={() => setLeaving(undefined)}
        onLeave={leave}
      />
    </>
  )
}
