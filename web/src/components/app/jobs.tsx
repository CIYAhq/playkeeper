import { useEffect, useRef } from 'react'
import type { Operation, ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { crashSummary } from '@/lib/crash'
import { formatMs } from '@/lib/format'
import { opLabel } from '@/lib/phase'

function finished(op: Operation, server: ServerStatus, machine: string) {
  const name = server.name
  if (op.status === 'failed') {
    const crash = server.crash?.start && Date.parse(server.crash.at) >= Date.parse(op.startedAt) ? server.crash : undefined
    toastManager.add({ title: t('op.failed', { what: opLabel(op, name) }), description: crash ? crashSummary(crash, name, machine) : op.error, type: 'error', timeout: 10_000 })
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
  }
}

/** Long jobs run in the background; say when one finishes, wherever the admin is. */
export function useJobToasts() {
  const { servers, machineName } = useWorkspace()
  const running = useRef(new Map<string, { op: Operation; server: ServerStatus }>())
  useEffect(() => {
    if (!servers) return
    const seen = new Set<string>()
    for (const s of servers) {
      seen.add(s.id)
      const was = running.current.get(s.id)
      if (was && s.operation?.id !== was.op.id && s.lastOperation?.id === was.op.id) finished(s.lastOperation, s, machineName)
      if (s.operation) running.current.set(s.id, { op: s.operation, server: s })
      else running.current.delete(s.id)
    }
    for (const [id, was] of running.current) {
      if (seen.has(id)) continue
      running.current.delete(id)
      if (was.op.kind === 'delete') toastManager.add({ title: t('settings.deletedToast', { server: was.server.name }), type: 'success' })
    }
  }, [servers, machineName])
}
