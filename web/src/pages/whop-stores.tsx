import { useId, useState, type FormEvent } from 'react'
import { BanIcon, CircleCheckIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { StoresResponse, SuspendableStore } from '@/api/types'
import { errorText } from '@/api/workspace'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/**
 * The businesses that sell from this dashboard through the Playkeeper Cloud
 * app, each with its customers, for the owner to suspend one with every
 * customer of it, or lift that.
 */
export function StoreSuspensions() {
  const id = useId()
  const stores = usePoll(() => get<StoresResponse>('/api/whop/stores'), 30_000)
  const [picked, setPicked] = useState<SuspendableStore>()
  const list = stores.data?.stores ?? []
  if (list.length === 0) return null
  return (
    <section aria-labelledby={`${id}-title`} className="mt-4">
      <h3 id={`${id}-title`} className="text-[13px] font-semibold">
        {t('whop.stores.title')}
      </h3>
      <ul className="divide-y divide-border">
        {list.map((s) => (
          <StoreRow key={s.id} store={s} onPick={() => setPicked(s)} />
        ))}
      </ul>
      <StoreSuspendDialog store={picked} onClose={() => setPicked(undefined)} onChanged={stores.refresh} />
    </section>
  )
}

/** Where a store stands: it left, or it's suspended, closed, needs a look or sells. */
function storeState(s: SuspendableStore): string {
  if (s.leftAt) return s.leftWhy ? t('whop.stores.left', { why: s.leftWhy }) : t('whop.stores.leftNoWhy')
  if (s.suspendedAt) return t('whop.stores.suspended', { reason: s.suspendReason ?? '' })
  if (s.closedWhy) return t('whop.stores.closed', { why: s.closedWhy })
  if (s.problem) return t('whop.stores.needsLook', { problem: s.problem })
  return t('whop.stores.selling')
}

function StoreRow({ store: s, onPick }: { store: SuspendableStore; onPick: () => void }) {
  return (
    <li className="flex items-center justify-between gap-3 py-2.5">
      <span className="min-w-0">
        <span className="block truncate text-[13px] font-semibold">{s.title}</span>
        <span className={cn('block truncate text-xs', s.leftAt || s.suspendedAt || s.closedWhy || s.problem ? 'text-warning-foreground' : 'text-muted-foreground')}>
          {t('whop.stores.customers', { count: s.customers })}
          {t('common.dot')}
          {storeState(s)}
        </span>
      </span>
      {s.suspendedAt ? (
        <Button variant="outline" size="sm" onClick={onPick}>
          <CircleCheckIcon />
          {t('whop.stores.lift')}
        </Button>
      ) : (
        !s.leftAt && (
          <Button variant="ghost" size="sm" onClick={onPick}>
            <BanIcon />
            {t('whop.stores.suspend')}
          </Button>
        )
      )}
    </li>
  )
}

/** Suspending a store with the owner's reason, or lifting its suspension. */
function StoreSuspendDialog({ store, onClose, onChanged }: { store: SuspendableStore | undefined; onClose: () => void; onChanged: () => Promise<void> }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const [reason, setReason] = useState('')
  const [shown, setShown] = useState<SuspendableStore>()
  if (store && store !== shown) {
    setShown(store)
    setReason('')
  }
  const s = store ?? shown
  const name = s?.title ?? ''
  const lifting = !!s?.suspendedAt

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!s || (!lifting && !reason.trim())) return
    setBusy(true)
    try {
      const path = `/api/whop/stores/${encodeURIComponent(s.id)}/suspension`
      if (lifting) {
        await del(path)
        toastManager.add({ title: t('whop.stores.liftedToast', { store: name }), type: 'success' })
      } else {
        await post(path, { reason: reason.trim() })
        toastManager.add({ title: t('whop.stores.suspendedToast', { store: name }), type: 'success' })
      }
      onClose()
      await onChanged()
    } catch (err) {
      toastManager.add({ title: errorText(err), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={!!store} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]">
        <form onSubmit={(e) => void submit(e)} className="contents" noValidate>
          <DialogHeader>
            <DialogTitle className="text-lg font-bold">{lifting ? t('whop.stores.liftTitle', { store: name }) : t('whop.stores.suspendTitle', { store: name })}</DialogTitle>
            <DialogDescription className="text-[13px]">{lifting ? t('whop.stores.liftBody') : t('whop.stores.suspendBody')}</DialogDescription>
          </DialogHeader>
          {!lifting && (
            <DialogPanel>
              <label className="flex flex-col gap-1.5">
                <span className="text-[13px] font-semibold">{t('whop.stores.reason')}</span>
                <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} placeholder={t('whop.stores.reasonPlaceholder')} autoComplete="off" className="max-sm:h-11 max-sm:[&>input]:h-full" />
              </label>
            </DialogPanel>
          )}
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
              {t('common.cancel')}
            </Button>
            {lifting ? (
              <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy}>
                <CircleCheckIcon />
                {t('whop.stores.liftConfirm')}
              </Button>
            ) : (
              <Button type="submit" variant="destructive" size={phone ? 'touch' : 'default'} loading={busy} disabledReason={reason.trim() ? undefined : t('reason.sayWhy')}>
                <BanIcon />
                {t('whop.stores.suspendConfirm', { store: name })}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  )
}
