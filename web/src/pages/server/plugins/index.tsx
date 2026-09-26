import { useEffect, useState } from 'react'
import type { ServerStatus } from '@/api/types'
import { addonKind, addonTab } from '@/lib/addons'
import { navigate, type ServerSub } from '@/lib/router'
import { cn } from '@/lib/utils'
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
  // The server page already animates the tab's first view; only a switch
  // between the list and the library animates here.
  const view = sub === 'browse' ? 'browse' : 'installed'
  const [firstView] = useState(view)
  const [switched, setSwitched] = useState(false)
  if (view !== firstView && !switched) setSwitched(true)
  if (!kind || right !== tab) return null
  return (
    <AddonsProvider server={server} kind={kind}>
      <div key={view} className={cn('flex flex-1 flex-col', switched && 'animate-page')}>
        {view === 'browse' ? <BrowseView /> : <InstalledView />}
      </div>
      <DetailSheet />
      <JobDialog />
      <RemoveDialog />
      <UpdateAskDialog />
      <VoiceChatDialog />
    </AddonsProvider>
  )
}
