import { useState } from 'react'
import { Trash2Icon } from 'lucide-react'
import { post } from '@/api/client'
import type { ServerStatus } from '@/api/types'
import { errorText, serverApi } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { navigate } from '@/lib/router'

/** Deletes a server once its name is typed, then goes Home. Settings and a create that didn't finish both open it. */
export function DeleteServerDialog({ server: s, open, onOpenChange }: { server: ServerStatus; open: boolean; onOpenChange: (open: boolean) => void }) {
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  async function remove() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/delete'), { confirm: typed.trim() })
      onOpenChange(false)
      navigate({ name: 'home' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[480px]">
        <div className="flex items-start gap-4 px-6 pt-6 pb-2">
          <Pip pose="hurt" size={52} />
          <div className="min-w-0 pt-1">
            <DialogTitle className="text-lg font-bold">{t('settings.deleteDialog', { server: s.name })}</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px]">{t('settings.deleteDialogBody')}</DialogDescription>
          </div>
        </div>
        <DialogPanel className="pt-3">
          <label className="flex flex-col gap-1.5 text-[13px]">
            <span>{rich('settings.deleteType', { b: (chunk) => <strong className="font-semibold">{chunk}</strong> }, { server: s.name })}</span>
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
          </label>
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={remove} loading={busy} disabledReason={typed.trim() === s.name ? undefined : t('settings.deleteTypeFirst', { server: s.name })}>
            <Trash2Icon />
            {t('settings.deleteConfirm', { server: s.name })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
