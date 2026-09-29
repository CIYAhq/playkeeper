import { useEffect, useId, useState, type FormEvent } from 'react'
import { ExternalLinkIcon, KeyRoundIcon, RefreshCwIcon, UnplugIcon } from 'lucide-react'
import { ApiError, del, get, post, put } from '@/api/client'
import type { WhopBuyer, WhopPlan, WhopStore } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, Marker } from '@/components/app/bits'
import { ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { allowanceText } from '@/lib/access'
import { formatDate, formatMB, relativeTime } from '@/lib/format'
import { linkProps } from '@/lib/router'

const whopDeveloper = 'https://whop.com/dashboard/developer'
const whopBlueprints = 'https://whop.com/blueprints'

const planServers = ['1', '2', '3', '4', '5']
const planMemory = ['2048', '3072', '4096', '6144', '8192', '12288', '16384', '24576', '32768']

/**
 * Settings › Sell on Whop: selling this machine's servers through a store on
 * Whop. Whop takes the payments; each plan a buyer pays for lets them create
 * servers here inside its allowance, as a creator does. The owner connects a
 * Whop API key, and the page shows the store's plans with what each allows.
 */
export function SellOnWhopSection() {
  const [store, setStore] = useState<WhopStore>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    let cancelled = false
    get<WhopStore>('/api/whop')
      .then((s) => !cancelled && setStore(s))
      .catch((e: unknown) => !cancelled && setError(errorText(e)))
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <Card as="section" aria-labelledby="whop-title" id="whop" className="scroll-mt-4">
      <CardTitle id="whop-title" className="max-sm:sr-only">
        {t('whop.title')}
      </CardTitle>
      {store ? (
        store.connected ? (
          <Connected store={store} onChange={setStore} />
        ) : (
          <Connect store={store} onChange={setStore} />
        )
      ) : error ? (
        <p className="mt-2 text-[13px] text-destructive-foreground">{error}</p>
      ) : (
        <div className="py-3">
          <LoadingLabel />
          <Skeleton className="h-4 w-48" />
          <Skeleton className="mt-1.5 h-3.5 w-64" />
        </div>
      )}
    </Card>
  )
}

/** Where the machine's address is set, for a store that can't take orders without one. */
function AddressLine({ store }: { store: WhopStore }) {
  const ws = useWorkspace()
  if (store.dashboard) return null
  return (
    <p className="mt-2 text-[13px] text-muted-foreground">
      {t('whop.needsAddress')}
      {ws.machine && (
        <a {...linkProps({ name: 'machine-settings', id: ws.machine.id })} className="ml-2 font-medium text-success-strong hover:underline">
          {t('whop.setAddress')}
        </a>
      )}
    </p>
  )
}

function Connect({ store, onChange }: { store: WhopStore; onChange: (s: WhopStore) => void }) {
  return (
    <div className="animate-fade">
      <p className="mt-1 text-[13px] text-muted-foreground">{t('whop.intro')}</p>
      <AddressLine store={store} />
      <ol className="mt-3 flex flex-col gap-2 text-[13px]">
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">1.</span>
          <span>
            {t('whop.step1')}
            <a href={whopBlueprints} target="_blank" rel="noreferrer" className="ml-2 inline-flex items-center gap-1 font-medium text-success-strong hover:underline">
              {t('whop.openBlueprints')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
          </span>
        </li>
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">2.</span>
          <span className="min-w-0">
            {t('whop.step2')}
            <a href={whopDeveloper} target="_blank" rel="noreferrer" className="ml-2 inline-flex items-center gap-1 font-medium text-success-strong hover:underline">
              {t('whop.openDeveloper')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
            <span className="mt-1 block font-mono text-xs break-words text-muted-foreground">{store.needs.join(', ')}</span>
          </span>
        </li>
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">3.</span>
          {t('whop.step3')}
        </li>
      </ol>
      <KeyForm onSaved={onChange} />
    </div>
  )
}

function KeyForm({ onSaved, onCancel }: { onSaved: (s: WhopStore) => void; onCancel?: () => void }) {
  const id = useId()
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [refused, setRefused] = useState<string>()

  async function save(e: FormEvent) {
    e.preventDefault()
    if (!key.trim()) return
    setBusy(true)
    setRefused(undefined)
    try {
      const s = await post<WhopStore>('/api/whop/connect', { key: key.trim() })
      toastManager.add({ title: t('whop.connected', { account: s.account?.title ?? '' }), type: 'success' })
      onSaved(s)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'whop_key_refused') setRefused(t('whop.refused'))
      else if (err instanceof ApiError && err.code === 'whop_permissions') setRefused(t('whop.missing', { permissions: String(err.params?.missing ?? '').split(',').join(', ') }))
      else toastManager.add({ title: errorText(err), type: 'error' })
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save} className="mt-3">
      <div className="flex gap-2 max-sm:flex-col sm:flex-wrap">
        <InputGroup className="max-sm:h-11 sm:min-w-[240px] sm:flex-1">
          <InputGroupAddon>
            <KeyRoundIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            type="password"
            value={key}
            onChange={(e) => {
              setKey(e.target.value)
              setRefused(undefined)
            }}
            placeholder={t('whop.keyPlaceholder')}
            aria-label={t('whop.keyLabel')}
            aria-invalid={refused ? true : undefined}
            aria-describedby={refused ? `${id}-refused` : `${id}-stays`}
            autoComplete="off"
            spellCheck={false}
          />
        </InputGroup>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel} className="max-sm:h-11">
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" loading={busy} disabledReason={key.trim() ? undefined : t('reason.pasteKey')} className="max-sm:h-11">
          {t('whop.connect')}
        </Button>
      </div>
      {refused ? (
        <p id={`${id}-refused`} className="mt-2 animate-fade text-xs text-destructive-foreground" role="alert">
          {refused}
        </p>
      ) : (
        <p id={`${id}-stays`} className="mt-2 text-xs text-muted-foreground">
          {t('whop.keyStays')}
        </p>
      )}
    </form>
  )
}

function Connected({ store, onChange }: { store: WhopStore; onChange: (s: WhopStore) => void }) {
  const [replacing, setReplacing] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [disconnecting, setDisconnecting] = useState(false)
  const [editing, setEditing] = useState<WhopPlan>()
  const selling = store.plans.filter((p) => p.allowance)

  async function sync() {
    setSyncing(true)
    try {
      onChange(await post<WhopStore>('/api/whop/sync'))
      toastManager.add({ title: t('whop.synced'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div className="animate-fade">
      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-2 py-2">
        <div className="min-w-0 flex-1">
          <p className="flex items-baseline gap-2 text-[13px] leading-5 font-semibold">
            {store.account?.title}
            <Marker tone={store.problem ? 'amber' : 'green'}>{store.problem ? t('whop.needsLook') : t('whop.on')}</Marker>
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('whop.keyEnding', { ending: store.keyEnding ?? '' })}
            {store.syncedAt && `${t('common.dot')}${t('whop.syncedAt', { when: relativeTime(store.syncedAt) })}`}
          </p>
        </div>
        {!replacing && (
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setReplacing(true)}>
              {t('whop.replace')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setDisconnecting(true)}>
              <UnplugIcon />
              {t('whop.disconnect')}
            </Button>
          </div>
        )}
      </div>
      {replacing && (
        <KeyForm
          onSaved={(s) => {
            setReplacing(false)
            onChange(s)
          }}
          onCancel={() => setReplacing(false)}
        />
      )}
      {store.problem && (
        <p className="mt-1 text-xs text-destructive-foreground" role="alert">
          {store.problem}
        </p>
      )}
      <AddressLine store={store} />
      {store.dashboard && <p className="mt-2 text-xs text-muted-foreground">{selling.length > 0 ? t('whop.selling', { dashboard: store.dashboard }) : t('whop.noneSelling')}</p>}
      <div className="mt-3 flex items-center justify-between gap-3">
        <h3 className="text-[13px] font-semibold">{t('whop.plans')}</h3>
        <Button variant="ghost" size="sm" onClick={() => void sync()} loading={syncing}>
          <RefreshCwIcon />
          {t('whop.sync')}
        </Button>
      </div>
      {store.plans.length === 0 ? (
        <p className="py-3 text-[13px] text-muted-foreground">{t('whop.noPlans')}</p>
      ) : (
        <ul className="divide-y divide-border">
          {store.plans.map((p) => (
            <PlanRow key={p.id} plan={p} onEdit={() => setEditing(p)} />
          ))}
        </ul>
      )}
      <h3 className="mt-4 text-[13px] font-semibold">{t('whop.buyers')}</h3>
      {store.dashboard && !store.webhook && <p className="mt-1 text-xs text-muted-foreground">{t('whop.noWebhook')}</p>}
      {store.buyers.length === 0 ? (
        <p className="py-3 text-[13px] text-muted-foreground">{t('whop.noBuyers')}</p>
      ) : (
        <ul className="divide-y divide-border">
          {store.buyers.map((b) => (
            <BuyerRow key={b.whopUserId} buyer={b} />
          ))}
        </ul>
      )}
      <AllowanceDialog plan={editing} onClose={() => setEditing(undefined)} onSaved={onChange} />
      <DisconnectDialog open={disconnecting} account={store.account?.title ?? ''} onClose={() => setDisconnecting(false)} onDone={onChange} />
    </div>
  )
}

function PlanRow({ plan, onEdit }: { plan: WhopPlan; onEdit: () => void }) {
  const hidden = plan.visibility !== 'visible'
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 py-3">
      <div className="min-w-0 flex-1">
        <p className="flex items-baseline gap-2 text-[13px] leading-5 font-semibold">
          {plan.title || plan.productTitle}
          <Marker>{hidden ? t('whop.hidden') : t('whop.visible')}</Marker>
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          {plan.price}
          {plan.trialDays ? `${t('common.dot')}${t('whop.trial', { count: plan.trialDays })}` : ''}
          {t('common.dot')}
          {plan.allowance ? allowanceText(plan.allowance) : t('whop.noAllowance')}
          {plan.allowanceFrom === 'store' && `${t('common.dot')}${t('whop.fromStore')}`}
        </p>
      </div>
      {plan.allowanceFrom !== 'store' && (
        <Button variant="outline" size="sm" onClick={onEdit}>
          {plan.allowance ? t('whop.changeAllowance') : t('whop.setAllowance')}
        </Button>
      )}
    </li>
  )
}

function buyerStatus(b: WhopBuyer): string {
  switch (b.status) {
    case 'invited':
      return b.invitedAt ? t('whop.buyer.invited', { when: relativeTime(b.invitedAt) }) : t('whop.buyer.sending')
    case 'joined':
      return t('whop.buyer.joined', { account: b.account ?? '' })
    case 'paused':
      return b.deletesAt ? t('whop.buyer.paused', { account: b.account ?? '', when: formatDate(b.deletesAt) }) : t('whop.buyer.ended')
    case 'deleted':
      return t('whop.buyer.deleted', { account: b.account ?? '' })
    case 'removed':
      return t('whop.buyer.removed')
    case 'turned_off':
      return t('whop.buyer.turnedOff')
    case 'sending':
      return t('whop.buyer.sending')
    case 'ended':
      return t('whop.buyer.ended')
    default: {
      const unreachable: never = b.status
      return unreachable
    }
  }
}

function BuyerRow({ buyer }: { buyer: WhopBuyer }) {
  return (
    <li className="py-3">
      <p className="text-[13px] leading-5 font-semibold">{buyer.username || buyer.whopUserId}</p>
      <p className="mt-1 text-xs text-muted-foreground">
        {buyerStatus(buyer)}
        {buyer.allowance && `${t('common.dot')}${allowanceText(buyer.allowance)}`}
      </p>
      {buyer.problem && <p className="mt-1 text-xs text-destructive-foreground">{buyer.problem}</p>}
    </li>
  )
}

function AllowanceDialog({ plan, onClose, onSaved }: { plan: WhopPlan | undefined; onClose: () => void; onSaved: (s: WhopStore) => void }) {
  const phone = useIsPhone()
  const [shown, setShown] = useState<WhopPlan>()
  const [servers, setServers] = useState('1')
  const [memory, setMemory] = useState('4096')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  if (plan && plan !== shown) {
    setShown(plan)
    setServers(String(plan.allowance?.servers ?? 1))
    setMemory(String(plan.allowance?.memoryMB ?? 4096))
    setError(undefined)
  }
  const p = plan ?? shown

  async function save(e: FormEvent, clear = false) {
    e.preventDefault()
    if (!p) return
    setBusy(true)
    setError(undefined)
    try {
      const body = clear ? { servers: 0, memoryMB: 0 } : { servers: Number(servers), memoryMB: Number(memory) }
      onSaved(await put<WhopStore>(`/api/whop/plans/${p.id}`, body))
      toastManager.add({ title: t('whop.allowanceSaved', { plan: p.title || p.productTitle }), type: 'success' })
      onClose()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={!!plan} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]">
        <form onSubmit={(e) => void save(e)} className="contents" noValidate>
          <DialogHeader>
            <DialogTitle className="text-lg font-bold">{t('whop.allowanceTitle', { plan: p?.title || p?.productTitle || '' })}</DialogTitle>
            <DialogDescription className="text-[13px]">{t('whop.allowanceHint')}</DialogDescription>
          </DialogHeader>
          <DialogPanel className="flex flex-col gap-4">
            <div className="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
              <div className="flex flex-col gap-1.5">
                <span className="text-[13px] font-semibold">{t('team.creatorServers')}</span>
                <ChoiceSelect value={servers} onChange={setServers} options={planServers.map((v) => ({ value: v, label: t('unit.servers', { count: Number(v) }) }))} label={t('team.creatorServers')} className="w-full min-w-0" />
              </div>
              <div className="flex flex-col gap-1.5">
                <span className="text-[13px] font-semibold">{t('team.creatorMemory')}</span>
                <ChoiceSelect value={memory} onChange={setMemory} options={planMemory.map((v) => ({ value: v, label: formatMB(Number(v)) }))} label={t('team.creatorMemory')} className="w-full min-w-0" />
              </div>
            </div>
            {error && (
              <p className="text-[13px] text-destructive-foreground" role="alert">
                {error}
              </p>
            )}
          </DialogPanel>
          <DialogFooter variant="bare" className="border-t border-border pt-4 sm:mx-6 sm:items-center sm:justify-between sm:px-0">
            {p?.allowance ? (
              <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={(e) => void save(e, true)} disabled={busy}>
                {t('whop.clearAllowance')}
              </Button>
            ) : (
              <span />
            )}
            <div className="flex gap-2 max-sm:flex-col-reverse">
              <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy}>
                {t('common.save')}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  )
}

function DisconnectDialog({ open, account, onClose, onDone }: { open: boolean; account: string; onClose: () => void; onDone: (s: WhopStore) => void }) {
  const [busy, setBusy] = useState(false)
  async function disconnect() {
    setBusy(true)
    try {
      const s = await del<WhopStore>('/api/whop')
      onDone(s)
      toastManager.add(s.notice ? { title: t('whop.disconnected'), description: s.notice, type: 'warning' } : { title: t('whop.disconnected'), type: 'success' })
      onClose()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('whop.disconnectTitle', { account })}</DialogTitle>
          <DialogDescription className="text-[13px]">{t('whop.disconnectBody')}</DialogDescription>
        </DialogHeader>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={() => void disconnect()} loading={busy}>
            <UnplugIcon />
            {t('whop.disconnect')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
