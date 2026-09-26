import { useState } from 'react'
import { SunIcon } from 'lucide-react'
import type { ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { serverAction } from '@/components/app/server-action'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatMB } from '@/lib/format'
import { whyNot } from '@/lib/phase'

// Wave 7: what Home shows of a sleeping server, apart from the server page's
// own sleep code (pages/server/sleep.tsx) so Home loads none of it.

/** Home's card detail for a sleeping server: "Asleep · wakes on join" and Wake up. */
export function AsleepDetail({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  return (
    <>
      <Pip pose="sleep" size={40} />
      <span className="text-[13px] text-muted-foreground">{s.sleep?.listening === false ? t('sleep.cardDeaf') : t('sleep.card')}</span>
      {!s.operation && can(ws.me, 'servers.run') && (
        <Button
          variant="outline"
          size="sm"
          className="relative z-10 ml-auto"
          loading={busy}
          disabledReason={whyNot(s, 'start', ws.stale)}
          onClick={async () => {
            setBusy(true)
            await serverAction(s, 'start')
            setBusy(false)
          }}
        >
          <SunIcon />
          {t('sleep.wakeShort')}
        </Button>
      )}
    </>
  )
}

/** "Survival gave back 4 GB", for the machine's memory line. */
export function gaveBackText(servers: ServerStatus[] | undefined, sleepingMemoryMB: number | undefined): string | undefined {
  if (!sleepingMemoryMB) return undefined
  const asleep = (servers ?? []).filter((s) => s.phase === 'asleep')
  const memory = formatMB(sleepingMemoryMB)
  return asleep.length === 1 && asleep[0] ? t('sleep.gaveBack', { server: asleep[0].name, memory }) : t('sleep.gaveBackMany', { memory })
}
