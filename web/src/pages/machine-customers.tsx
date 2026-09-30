import { useEffect, useState } from 'react'
import { get, post } from '@/api/client'
import type { MachineCustomer, MachineView } from '@/api/types'
import { errorText, machineApi, useWorkspace, type Workspace } from '@/api/workspace'
import { Card, CardTitle, Spinner } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Radio, RadioGroupPrimitive } from '@/components/ui/radio-group'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatMB } from '@/lib/format'
import { machineLabel } from '@/lib/machines'
import { usePoll } from '@/lib/usePoll'

/** How often a machine's customers are read again: often while one moves there, so the list follows. */
const customersMs = 30_000
const movingMs = 3000

/** How the owner's pages name a machine: the dashboard's own by the dashboard's name. */
function nameOf(ws: Workspace, x?: MachineView): string {
  return (x?.kind === 'local' ? ws.machineName : machineLabel(x)) || x?.id || ''
}

/**
 * A customer's plan and servers, and where their move stands. One whose
 * servers go on another machine, or who waits for room, is on m only
 * because some of their servers are still there.
 */
function customerLine(c: MachineCustomer, m: MachineView, ws: Workspace): string {
  const parts = [t('machines.customers.planLine', { memory: formatMB(c.memoryMB), dot: t('common.dot'), servers: t('machines.customers.servers', { count: c.servers }) })]
  if (c.state === 'paused') parts.push(t('machines.customers.paused'))
  if (c.state === 'suspended') parts.push(t('machines.customers.suspended'))
  const theirs = c.machineId === m.id
  if (!theirs) {
    const home = ws.machines.find((x) => x.id === c.machineId)
    parts.push(c.machineId ? t('machines.customers.leftHere', { count: c.here, name: nameOf(ws, home) || c.machineId }) : t('machines.customers.leftWaiting', { count: c.here }))
  }
  if (c.move?.error) parts.push(t(theirs ? 'machines.customers.moveStopped' : 'machines.customers.moveStoppedElsewhere', { error: c.move.error }))
  else if (c.move) parts.push(t(theirs ? 'machines.customers.moving' : 'machines.customers.movingElsewhere', { count: c.move.left }))
  return parts.join(t('common.dot'))
}

/**
 * The customers whose servers go on a machine, or who still have servers
 * there, each with Move…, for the owner: in a card of its own on the
 * dashboard's machine's page, and in the Customers card on a joined
 * machine's. Nothing shows while it has none.
 */
export function CustomerList({ machine: m, card = false }: { machine: MachineView; card?: boolean }) {
  const ws = useWorkspace()
  const [interval, setIntervalMs] = useState(customersMs)
  const list = usePoll(() => get<MachineCustomer[]>(machineApi(m.id, '/customers')), interval, m.id)
  const [moving, setMoving] = useState<MachineCustomer>()
  const [again, setAgain] = useState<number>()
  const underway = !!list.data?.some((c) => c.move && !c.move.error)
  useEffect(() => setIntervalMs(underway ? movingMs : customersMs), [underway])
  async function tryAgain(c: MachineCustomer) {
    setAgain(c.id)
    try {
      await post(`/api/customers/${c.id}/move`, c.machineId ? { machineId: c.machineId } : {})
      void list.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setAgain(undefined)
    }
  }
  if (!list.data?.length) return null
  const rows = (
    <>
      <ul className="mt-3 flex flex-col">
        {list.data.map((c) => (
          <li key={c.id} className="flex items-center gap-3 border-t border-border py-2 text-[13px] first:border-t-0">
            <span className="min-w-0 flex-1">
              <span className="block font-medium">{c.name}</span>
              <span className="block text-xs text-muted-foreground">{customerLine(c, m, ws)}</span>
            </span>
            {c.move && !c.move.error ? (
              <Spinner className="size-4 shrink-0" />
            ) : c.move?.error ? (
              <Button variant="outline" size="sm" loading={again === c.id} onClick={() => void tryAgain(c)}>
                {t('machines.customers.moveAgain')}
              </Button>
            ) : (
              <Button variant="outline" size="sm" onClick={() => setMoving(c)}>
                {t('machines.customers.move')}
              </Button>
            )}
          </li>
        ))}
      </ul>
      <MoveDialog customer={moving} from={m} onClose={() => setMoving(undefined)} onMoved={() => void list.refresh()} />
    </>
  )
  return card ? (
    <Card aria-labelledby="machine-customers">
      <CardTitle id="machine-customers">{t('machines.customers.title')}</CardTitle>
      {rows}
    </Card>
  ) : (
    rows
  )
}

/** The move dialog's choice of the fullest machine with room, the dashboard's pick. */
const fullest = 'fullest'

/**
 * Moving a customer off a machine: to the fullest other machine with room for their plan, or one the owner picks.
 * One whose servers go on another machine goes back there by default, which brings theirs together, whether or not
 * it takes new customers.
 */
function MoveDialog({ customer, from, onClose, onMoved }: { customer?: MachineCustomer; from: MachineView; onClose: () => void; onMoved: () => void }) {
  const ws = useWorkspace()
  const [to, setTo] = useState(fullest)
  const [busy, setBusy] = useState(false)
  const home = customer?.machineId && customer.machineId !== from.id ? customer.machineId : ''
  useEffect(() => {
    if (customer) setTo(home || fullest)
  }, [customer, home])
  const targets = ws.machines.filter((x) => x.id !== from.id && (x.kind === 'local' || x.takesCustomers || x.id === home))
  const who = customer?.name ?? ''
  async function move() {
    if (!customer) return
    setBusy(true)
    try {
      const r = await post<{ machineId: string }>(`/api/customers/${customer.id}/move`, to === fullest ? {} : { machineId: to })
      toastManager.add({ title: t('machines.customers.moveStarted', { customer: customer.name, name: nameOf(ws, ws.machines.find((x) => x.id === r.machineId)) || r.machineId }), type: 'success' })
      onClose()
      onMoved()
      void ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={!!customer} onOpenChange={(open) => !open && onClose()}>
      <DialogPopup className="sm:max-w-[500px]" showCloseButton={false}>
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('machines.customers.moveTitle', { customer: who })}</DialogTitle>
          <DialogDescription>{t('machines.customers.moveBody')}</DialogDescription>
        </DialogHeader>
        <fieldset className="mx-6 mb-4 flex flex-col">
          <legend className="mb-2 text-[13px] font-semibold">{t('machines.customers.moveTo')}</legend>
          <RadioGroupPrimitive value={to} onValueChange={(v) => setTo(String(v))} aria-label={t('machines.customers.moveTo')} className="flex flex-col gap-3">
            <label className="flex items-center gap-2.5 text-[13px] max-sm:text-[15px]">
              <Radio value={fullest} />
              {t('machines.customers.moveFullest')}
            </label>
            {targets.map((x) => (
              <label key={x.id} className="flex items-center gap-2.5 text-[13px] max-sm:text-[15px]">
                <Radio value={x.id} />
                {x.id === home ? t('machines.customers.moveHome', { name: nameOf(ws, x) }) : nameOf(ws, x)}
              </label>
            ))}
          </RadioGroupPrimitive>
        </fieldset>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4">
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void move()} loading={busy}>
            {t('machines.customers.moveConfirm', { customer: who })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
