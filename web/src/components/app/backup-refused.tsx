import { useState } from 'react'
import { ArchiveIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { BackupRefusal, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Notice } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { phaseTone, whyNot } from '@/lib/phase'
import { past } from '@/lib/when'

/**
 * Scheduled backups refused because world saving couldn't be paused, shown
 * until a backup succeeds. "Back up now" stops a running server for the
 * backup, which needs no pause, and asks first; a server that isn't running
 * is backed up as it is.
 */
export function BackupRefusedNotice({ server: s, refusal: r, className }: { server: ServerStatus; refusal: BackupRefusal; className?: string }) {
  const ws = useWorkspace()
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const online = s.phase === 'online'
  const blocked = whyNot(s, online || phaseTone(s.phase) === 'busy' ? 'restart' : 'change', ws.stale)
  const last = s.lastBackup

  async function backUp(stopped: boolean) {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/backups'), stopped ? { stopped: true } : {})
      setConfirm(false)
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Notice
        tone="warning"
        stacked
        className={className}
        title={t('backupRefused.title', { count: r.count })}
        action={
          can(ws.me, 'backups.make') && (
            <Button size="sm" onClick={() => (online ? setConfirm(true) : void backUp(false))} loading={busy || s.operation?.kind === 'backup'} disabledReason={blocked}>
              <ArchiveIcon />
              {t('world.backUpNow')}
            </Button>
          )
        }
      >
        {`${r.kind === 'not_online' ? t('backupRefused.notOnline', { server: s.name }) : r.error} ${last ? t('backupRefused.last', { when: past(last.createdAt) }) : t('backupRefused.none')}`}
      </Notice>
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogPopup className="sm:max-w-[460px]">
          <DialogHeader>
            <DialogTitle className="text-lg font-bold">{t('backupRefused.stopTitle', { server: s.name })}</DialogTitle>
            <DialogDescription>{t('backupRefused.stopBody', { server: s.name })}</DialogDescription>
          </DialogHeader>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button variant="ghost" onClick={() => setConfirm(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void backUp(true)} loading={busy}>
              <ArchiveIcon />
              {t('backup.stopAndBackUp')}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </>
  )
}
