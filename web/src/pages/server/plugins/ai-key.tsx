import { useWorkspace } from '@/api/workspace'
import { AIKeyDialog, AIKeyEditor } from '@/components/app/ai-key'
import { useIsPhone } from '@/components/app/controls'
import { can } from '@/lib/access'
import { buildBattleProvider, isAIBuildBattle } from '@/lib/ai-keys'
import { cn } from '@/lib/utils'
import { useAddons } from './state'

/** AI Build Battle's key on its own, from its row's Add key. */
export function PluginKeyDialog() {
  const a = useAddons()
  return <AIKeyDialog server={a.server} keys={a.aiKeys} provider={buildBattleProvider} open={a.keyOpen} onOpenChange={a.openKey} />
}

/** The key in AI Build Battle's details once it's on the server, for an account that may change the server's files. */
export function PluginKeySection({ className }: { className?: string }) {
  const a = useAddons()
  const ws = useWorkspace()
  const phone = useIsPhone()
  if (!can(ws.me, 'files.edit') || !a.rows.some((r) => isAIBuildBattle(r.addon))) return null
  return (
    <section className={cn('border-t border-border pt-4', className)}>
      <AIKeyEditor server={a.server} keys={a.aiKeys} provider={buildBattleProvider} phone={phone} />
    </section>
  )
}
