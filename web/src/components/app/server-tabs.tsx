import type { ReactNode } from 'react'
import { GlobeIcon, LayoutGridIcon, PuzzleIcon, SlidersHorizontalIcon, SquareTerminalIcon, UsersIcon } from 'lucide-react'
import type { Me, ServerStatus } from '@/api/types'
import type { MessageKey } from '@/i18n'
import { can } from '@/lib/access'
import { addonTab } from '@/lib/addons'
import type { ServerTab } from '@/lib/router'

/** Every server tab, in the tab bar's order. */
export const serverTabs: { tab: ServerTab; key: MessageKey; icon: ReactNode }[] = [
  { tab: 'overview', key: 'tab.overview', icon: <LayoutGridIcon /> },
  { tab: 'console', key: 'tab.console', icon: <SquareTerminalIcon /> },
  { tab: 'players', key: 'tab.players', icon: <UsersIcon /> },
  { tab: 'world', key: 'tab.world', icon: <GlobeIcon /> },
  { tab: 'plugins', key: 'tab.plugins', icon: <PuzzleIcon /> },
  { tab: 'mods', key: 'tab.mods', icon: <PuzzleIcon /> },
  { tab: 'settings', key: 'tab.settings', icon: <SlidersHorizontalIcon /> },
]

/**
 * The tabs of s that me gets: Plugins, Mods and Settings need servers.manage,
 * and a server has Plugins or Mods by its type. The tab bar and the command
 * palette both offer exactly these.
 */
export function serverTabsFor(me: Me, s: ServerStatus) {
  return serverTabs
    .filter((x) => (x.tab !== 'settings' && x.tab !== 'plugins' && x.tab !== 'mods') || can(me, 'servers.manage'))
    .filter((x) => (x.tab !== 'plugins' && x.tab !== 'mods') || x.tab === addonTab(s.type))
}
