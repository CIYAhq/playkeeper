import { useId, useState } from 'react'
import { post } from '@/api/client'
import type { NetworkGuard } from '@/api/types'
import { machineApi, useWorkspace } from '@/api/workspace'
import { Card, CardTitle } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { Group } from '../more'
import { ErrorLine, refusal } from '../two-factor'

/**
 * The owner's switch for keeping servers away from this machine, next to
 * the metadata block that is always on. The dashboard turns it on when it
 * makes a creator invite, and refuses to turn it off while there are
 * creators.
 */
export function NetworkGuardSettings({ id, guard }: { id: string; guard: NetworkGuard }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const switchId = useId()
  const [g, setG] = useState(guard)
  const [busy, setBusy] = useState(false)
  async function toggle(host: boolean) {
    setBusy(true)
    try {
      setG(await post<NetworkGuard>(machineApi(id, '/network-guard'), { host }))
    } catch (e) {
      toastManager.add({ title: refusal(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  const body = (
    <>
      <div className="flex min-h-12 items-center gap-4 py-2">
        <label htmlFor={switchId} className="min-w-0 flex-1">
          <span className="block text-[13px] leading-5 font-semibold">{t('guard.host')}</span>
          <span className="block text-xs text-muted-foreground">{t('guard.hostHint')}</span>
        </label>
        <Switch id={switchId} checked={g.host} disabled={busy || !can(ws.me, 'machine.manage')} onCheckedChange={(host) => void toggle(host)} />
      </div>
      <p className="text-xs text-muted-foreground">{t('guard.metadata')}</p>
      <ErrorLine text={g.problem && t('guard.problem', { problem: g.problem })} className="mt-1" />
    </>
  )
  if (phone) {
    return (
      <div className="mt-5">
        <Group label={t('guard.title')}>
          <li className="flex flex-col px-4 pb-3">{body}</li>
        </Group>
      </div>
    )
  }
  return (
    <Card className="mt-4">
      <CardTitle>{t('guard.title')}</CardTitle>
      {body}
    </Card>
  )
}
