import { useEffect } from 'react'
import type { ServerStatus } from '@/api/types'
import { addonKind, addonTab } from '@/lib/addons'
import { navigate, type ServerSub } from '@/lib/router'
import { PluginKeyDialog } from './ai-key'
import { BrowseView } from './browse'
import { DetailSheet } from './detail'
import { JobDialog, RemoveDialog, UpdateAskDialog } from './dialogs'
import { InstalledView } from './installed'
import { AddonsProvider } from './state'
import { VoiceChatDialog } from './voice'

type AddonTab = 'plugins' | 'mods'

/** The Plugins tab, or Mods on mod servers: what's installed, or the library. */
export function PluginsPage({ server, tab, sub }: { server: ServerStatus; tab: AddonTab; sub?: ServerSub }) {
  const kind = addonKind(server.type)
  const right = addonTab(server.type)
  // A Plugins link on a mod server lands on Mods, and the other way round;
  // servers with neither go to the overview.
  useEffect(() => {
    if (right === tab) return
    navigate(right ? { name: 'server', slug: server.slug, tab: right, sub: sub === 'browse' ? sub : undefined } : { name: 'server', slug: server.slug, tab: 'overview' }, true)
  }, [right, tab, sub, server.slug])
  const view = sub === 'browse' ? 'browse' : 'installed'
  if (!kind || right !== tab) return null
  return (
    <AddonsProvider server={server} kind={kind}>
      <div key={view} className="flex flex-1 flex-col">
        {view === 'browse' ? <BrowseView /> : <InstalledView />}
      </div>
      <DetailSheet />
      <JobDialog />
      <RemoveDialog />
      <UpdateAskDialog />
      <VoiceChatDialog />
      <PluginKeyDialog />
    </AddonsProvider>
  )
}
