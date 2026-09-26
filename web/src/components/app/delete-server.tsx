import { useState } from 'react'
import { DownloadIcon, Trash2Icon } from 'lucide-react'
import { ApiError, download, get, post } from '@/api/client'
import type { OffsiteView, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Notice } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { can } from '@/lib/access'
import { navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'

/** Copies somewhere else, kept or still made, that only the server's recovery key opens, while the key was never downloaded. */
function keyNotSaved(v: OffsiteView | undefined): boolean {
  return !!v?.key && !v.key.savedAt && (v.copies > 0 || v.enabled)
}

/** Deletes a server once its name is typed, then goes Home. Settings and a create that didn't finish both open it. */
export function DeleteServerDialog({ server: s, open, onOpenChange }: { server: ServerStatus; open: boolean; onOpenChange: (open: boolean) => void }) {
  const ws = useWorkspace()
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [withoutKey, setWithoutKey] = useState(false)
  const [savingKey, setSavingKey] = useState(false)
  // The agent's refusal when the key wasn't downloaded, for when the page's view of the copies is older.
  const [refused, setRefused] = useState<ApiError>()
  const off = usePoll(() => (open ? get<OffsiteView>(serverApi(s.id, '/offsite')) : Promise.resolve(undefined)), 30_000, open ? s.id : '')
  const keyRisk = keyNotSaved(off.data) || !!refused
  const place = off.data?.place || String(refused?.params?.place ?? '')
  const copies = off.data?.copies ?? Number(refused?.params?.copies ?? 0)
  function close() {
    onOpenChange(false)
    setWithoutKey(false)
    setRefused(undefined)
  }
  async function remove() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/delete'), keyRisk && withoutKey ? { confirm: typed.trim(), forgetKey: true } : { confirm: typed.trim() })
      close()
      navigate({ name: 'home' })
    } catch (e) {
      if (e instanceof ApiError && e.reason === 'recovery_key_not_saved') setRefused(e)
      else toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  async function saveKey() {
    const file = off.data?.key?.fileName
    if (!file) return
    setSavingKey(true)
    try {
      const name = await download(serverApi(s.id, '/offsite/recovery-key'), file)
      toastManager.add({ title: t('offsite.key.saved', { file: name }), type: 'success' })
      setRefused(undefined)
      await off.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setSavingKey(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => (o ? onOpenChange(true) : close())}>
      <DialogPopup className="sm:max-w-[480px]">
        <div className="flex items-start gap-4 px-6 pt-6 pb-2">
          <Pip pose="hurt" size={52} />
          <div className="min-w-0 pt-1">
            <DialogTitle className="text-lg font-bold">{t('settings.deleteDialog', { server: s.name })}</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px]">{t('settings.deleteDialogBody')}</DialogDescription>
          </div>
        </div>
        <DialogPanel className="flex flex-col gap-4 pt-3">
          {keyRisk && (
            <div className="flex flex-col gap-3 rounded-2xl border border-border p-3.5">
              <Notice
                tone="warning"
                stacked
                title={t('settings.deleteKeyTitle')}
                action={
                  off.data?.key && (
                    <Button size="sm" variant="outline" loading={savingKey} disabledReason={can(ws.me, 'backups.recovery_key') ? undefined : t('offsite.key.holdersOnly')} onClick={() => void saveKey()}>
                      <DownloadIcon />
                      {t('offsite.key.download')}
                    </Button>
                  )
                }
              >
                {t('settings.deleteKeyBody', { count: copies, server: s.name, place })}
              </Notice>
              <label className="flex items-start gap-2.5 text-[13px]">
                <Checkbox checked={withoutKey} onCheckedChange={(c) => setWithoutKey(c === true)} className="mt-0.5" />
                {t('settings.deleteWithoutKey')}
              </label>
            </div>
          )}
          <label className="flex flex-col gap-1.5 text-[13px]">
            <span>{rich('settings.deleteType', { b: (chunk) => <strong className="font-semibold">{chunk}</strong> }, { server: s.name })}</span>
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
          </label>
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={close}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="destructive"
            onClick={remove}
            loading={busy}
            disabledReason={typed.trim() !== s.name ? t('settings.deleteTypeFirst', { server: s.name }) : keyRisk && !withoutKey ? t('settings.deleteKeyFirst') : undefined}
          >
            <Trash2Icon />
            {t('settings.deleteConfirm', { server: s.name })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
