import { get } from '@/api/client'
import type { SaleRoom } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Card, CardTitle } from '@/components/app/bits'
import { LoadingLabel } from '@/components/app/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { formatMB } from '@/lib/format'
import { machineLabel } from '@/lib/machines'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** How often the card reads the room again while it's open. */
const refreshMs = 30_000

/**
 * Settings › Machines › Room for customers, the owner's: how many more of
 * each plan the store sells fit at once, which is each plan's stock on
 * Whop, and what each machine can still set aside for customers. Without
 * plans on sale there's nothing to show.
 */
export function SaleRoomCard() {
  const ws = useWorkspace()
  const room = usePoll(() => get<SaleRoom>('/api/machines/room'), refreshMs)
  const r = room.data
  if (r && r.plans.length === 0) return null
  const machineName = (id: string) => {
    const m = ws.machines.find((x) => x.id === id)
    return (m?.kind === 'local' ? ws.machineName : machineLabel(m)) || id
  }
  return (
    <Card aria-labelledby="room-title">
      <CardTitle id="room-title">{t('room.title')}</CardTitle>
      {r ? (
        <div className="animate-fade">
          <p className="mt-1 text-[13px] text-muted-foreground">{t('room.lead')}</p>
          <ul className="mt-3 flex flex-col">
            {r.plans.map((p) => (
              <li key={p.id} className="flex items-baseline justify-between gap-3 border-t border-border py-2 text-[13px] first:border-t-0">
                <span className="min-w-0">
                  <span className="font-semibold">{p.name}</span> <span className="text-xs text-muted-foreground">{p.free ? t('room.planFree', { memory: formatMB(p.memoryMB) }) : formatMB(p.memoryMB)}</span>
                </span>
                <span className={cn('shrink-0 font-medium', p.left === 0 && 'text-warning-strong')}>{p.left > 0 ? t('room.left', { count: p.left }) : t('room.soldOut')}</span>
              </li>
            ))}
          </ul>
          <h3 className="mt-3 text-xs font-semibold text-muted-foreground">{t('room.machines')}</h3>
          <ul className="mt-1 flex flex-col">
            {r.machines.map((m) => (
              <li key={m.id} className="flex items-baseline justify-between gap-3 py-1 text-[13px]">
                <span className="min-w-0 truncate">{machineName(m.id)}</span>
                <span className="shrink-0 text-right text-xs text-muted-foreground">{m.takes ? t('room.machineFree', { memory: formatMB(m.freeMB) }) : m.why}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : room.error ? (
        <p className="mt-2 text-[13px] text-destructive-foreground">{errorText(room.error)}</p>
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
