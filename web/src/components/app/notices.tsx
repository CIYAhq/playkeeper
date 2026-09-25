import { useState } from 'react'
import { ArchiveIcon, SquareTerminalIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { Operation, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Notice } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatClock, formatDate, sameDay } from '@/lib/format'
import { opLabel } from '@/lib/phase'
import { linkProps } from '@/lib/router'

const avoidedWhenStopped = ['unexpected_reply', 'save_timeout', 'file_changing', 'saving_resumed']

/**
 * Whether a failed backup is one a backup with the server stopped avoids:
 * the console answered something unexpected or too late, a plugin kept
 * writing, or something else turned saving back on.
 */
function stoppedBackupHelps(op: Operation): boolean {
  const kind = op.detail?.errorKind
  return op.kind === 'backup' && typeof kind === 'string' && avoidedWhenStopped.includes(kind)
}

/** Stops the server, backs it up and starts it again, without its console. */
function BackUpStoppedButton({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  async function go() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/backups'), { stopped: true })
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Button variant="outline" size="sm" onClick={go} loading={busy} disabled={ws.stale || !!s.operation}>
      <ArchiveIcon />
      {t('backup.stopAndBackUp')}
    </Button>
  )
}

/** What failed and the hint; a backup the console couldn't take offers one with the server stopped. */
export function FailedJobNotice({ server: s, op, onDismiss, className }: { server: ServerStatus; op: Operation; onDismiss: () => void; className?: string }) {
  return (
    <Notice
      tone="error"
      className={className}
      title={`${t('op.failed', { what: opLabel(op, s.name) })}: ${op.error ?? ''}`}
      action={
        <span className="flex items-center gap-2">
          {stoppedBackupHelps(op) && s.phase === 'online' && <BackUpStoppedButton server={s} />}
          <Button variant="ghost" size="sm" onClick={onDismiss}>
            {t('common.dismiss')}
          </Button>
        </span>
      }
    >
      {op.hint}
    </Notice>
  )
}

/** World saving stayed off after a backup: what could be lost, and the two ways to turn it back on. */
export function SavingPausedNotice({ server: s, className }: { server: ServerStatus; className?: string }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const since = s.savingPausedSince
  if (!since || ws.stale) return null
  const time = sameDay(new Date(since), new Date()) ? formatClock(since) : `${formatDate(since)} ${formatClock(since)}`
  async function resume() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/saving/resume'))
      toastManager.add({ title: t('backup.savingResumed'), type: 'success' })
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Notice
      tone="warning"
      stacked
      className={className}
      title={t('backup.savingPaused')}
      action={
        <span className="flex items-center gap-2">
          <Button variant="ghost" size="sm" render={<a {...linkProps({ name: 'server', slug: s.slug, tab: 'console' })} />}>
            <SquareTerminalIcon />
            {t('backup.openConsole')}
          </Button>
          <Button size="sm" onClick={resume} loading={busy} disabled={s.phase !== 'online' || !!s.operation}>
            {t('backup.resumeSaving')}
          </Button>
        </span>
      }
    >
      {t('backup.savingPausedBody', { server: s.name, time })}
    </Notice>
  )
}
