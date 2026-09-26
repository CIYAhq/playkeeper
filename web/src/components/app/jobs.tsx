import { useEffect, useRef } from 'react'
import type { Operation, ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatMs } from '@/lib/format'
import { opLabel } from '@/lib/phase'
import { href, navigate } from '@/lib/router'

/** Jobs a page is showing itself; a toast when one finishes would say it twice. */
export const jobsOnScreen = new Set<string>()

/** Where a "copy is ready" toast sends the admin: the World tab picks the restore up again. */
export const restoreCopyHash = '#restore-copy'

function finished(op: Operation, server: ServerStatus) {
  if (jobsOnScreen.has(op.id)) return
  const name = server.name
  if (op.status === 'failed') {
    toastManager.add({ title: t('op.failed', { what: opLabel(op, name) }), description: op.error, type: 'error', timeout: 10_000 })
    return
  }
  if (op.status === 'cancelled') {
    toastManager.add({ title: t('op.cancelled', { what: opLabel(op, name) }), description: t('offsiteRestore.nothingChanged') })
    return
  }
  const downtime = typeof op.detail?.downtimeMs === 'number' ? op.detail.downtimeMs : undefined
  switch (op.kind) {
    case 'create':
      toastManager.add({ title: t('creating.onlineToast', { server: name }), type: 'success' })
      return
    case 'backup':
      toastManager.add({ title: t('world.backupDone'), description: downtime !== undefined && downtime > 0 ? t('world.backupDoneBody', { server: name, time: formatMs(downtime) }) : undefined, type: 'success' })
      return
    case 'restore':
      toastManager.add({ title: t('toast.restored', { server: name }), type: 'success' })
      return
    case 'update-version':
      toastManager.add({ title: t('toast.versionDone', { server: name, version: server.config?.minecraftVersion ?? '' }), type: 'success' })
      return
    case 'offsite-restore':
      toastManager.add({
        title: t('offsiteRestore.ready'),
        description: t('offsiteRestore.body'),
        type: 'success',
        timeout: 0,
        actionProps: { children: t('offsiteRestore.inside'), onClick: () => navigate(href({ name: 'server', slug: server.slug, tab: 'world' }) + restoreCopyHash) },
      })
      return
    case 'offsite-check':
      toastManager.add({ title: t('world.copyCheckedToast'), type: 'success' })
      return
  }
}

/** Long jobs run in the background; say when one finishes, wherever the admin is. */
export function useJobToasts() {
  const { servers } = useWorkspace()
  const running = useRef(new Map<string, { op: Operation; server: ServerStatus }>())
  useEffect(() => {
    if (!servers) return
    const seen = new Set<string>()
    for (const s of servers) {
      seen.add(s.id)
      const was = running.current.get(s.id)
      if (was && s.operation?.id !== was.op.id && s.lastOperation?.id === was.op.id) finished(s.lastOperation, s)
      if (s.operation) running.current.set(s.id, { op: s.operation, server: s })
      else running.current.delete(s.id)
    }
    for (const [id, was] of running.current) {
      if (seen.has(id)) continue
      running.current.delete(id)
      if (was.op.kind === 'delete') toastManager.add({ title: t('settings.deletedToast', { server: was.server.name }), type: 'success' })
    }
  }, [servers])
}
