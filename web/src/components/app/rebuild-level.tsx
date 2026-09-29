import { useState } from 'react'
import { post } from '@/api/client'
import type { ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import type { FixPlan } from '@/lib/crash'

export type RebuildPlan = Extract<FixPlan, { kind: 'rebuild-level' }>

const staysKeys: [string, MessageKey][] = [
  ['game_rules', 'rebuildLevel.staysGameRules'],
  ['time', 'rebuildLevel.staysTime'],
  ['world_border', 'rebuildLevel.staysBorder'],
]

const resetKeys: Record<string, MessageKey> = {
  game_rules: 'rebuildLevel.resetGameRules',
  time: 'rebuildLevel.resetTime',
  world_border: 'rebuildLevel.resetBorder',
  spawn: 'rebuildLevel.resetSpawn',
}

const seedKeys: Record<string, MessageKey> = {
  world: 'rebuildLevel.seedWorld',
  backup: 'rebuildLevel.seedBackup',
  properties: 'rebuildLevel.seedProperties',
}

/**
 * What a new level.dat keeps and starts over, before the owner makes it:
 * the world is backed up, then Playkeeper deletes both damaged files and
 * starts the server, which makes a new one.
 */
export function RebuildLevelDialog({ server: s, plan, onClose }: { server: ServerStatus; plan: RebuildPlan | undefined; onClose: () => void }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const stays: MessageKey[] = ['rebuildLevel.staysBuilds', 'rebuildLevel.staysPlayers']
  const resets: MessageKey[] = []
  if (plan) {
    for (const [what, key] of staysKeys) if (!plan.resets.includes(what)) stays.push(key)
    const seed = seedKeys[plan.seedFrom]
    if (seed) stays.push(seed)
    for (const r of plan.resets) if (resetKeys[r]) resets.push(resetKeys[r])
    if (!seed) resets.push('rebuildLevel.resetSeed')
  }

  async function confirm() {
    if (!plan) return
    setBusy(true)
    try {
      await post(serverApi(s.id, '/world/rebuild-level'), { world: plan.world, start: true })
      toastManager.add({ title: t('rebuildLevel.started', { server: s.name }), type: 'success' })
      onClose()
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={!!plan} onOpenChange={(open) => !open && onClose()}>
      <DialogPopup className="sm:max-w-[520px]">
        <div className="flex items-start gap-4 px-6 pt-6 pb-2 max-sm:px-5">
          <Pip pose="hardhat" size={52} />
          <div className="min-w-0 pt-1">
            <DialogTitle className="text-xl leading-7 font-bold">{t('rebuildLevel.title', { server: s.name })}</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px]">{t('rebuildLevel.lead')}</DialogDescription>
          </div>
        </div>
        <DialogPanel className="pt-3 max-sm:px-5">
          <h3 className="text-[13px] font-semibold">{t('rebuildLevel.stays')}</h3>
          <ul className="mt-1.5 flex list-disc flex-col gap-1 pl-5 text-[13px] text-muted-foreground">
            {stays.map((k) => (
              <li key={k}>{t(k)}</li>
            ))}
          </ul>
          <h3 className="mt-4 text-[13px] font-semibold">{t('rebuildLevel.resets')}</h3>
          <ul className="mt-1.5 flex list-disc flex-col gap-1 pl-5 text-[13px] text-muted-foreground">
            {resets.map((k) => (
              <li key={k}>{t(k)}</li>
            ))}
          </ul>
          <p className="mt-4 text-[13px] text-muted-foreground">{t('rebuildLevel.backup')}</p>
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={confirm} loading={busy}>
            {t('rebuildLevel.confirm')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
