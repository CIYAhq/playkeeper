import type { Action, Me, TokenRole } from '@/api/types'
import type { MessageKey } from '@/i18n'
import type { Route } from './router'

/** Whether the signed-in account may take act, as the panel's permit decides. The panel checks every request anyway. */
export function can(me: Me, act: Action): boolean {
  switch (me.user.role) {
    case 'owner':
      return true
    case 'member':
      return act === 'view' || act === 'account.manage'
  }
  return false
}

/** The roles a token of this account may have: never more than the account itself. */
export function tokenRoles(me: Me): TokenRole[] {
  return me.user.role === 'owner' ? ['viewer', 'moderator', 'admin'] : ['viewer']
}

export type SettingsSectionName = 'ai-agents' | 'machines'

/** The sections of Settings, each for the accounts that may use it. */
export const settingsSections: { route: Route & { name: SettingsSectionName }; label: MessageKey; act: Action }[] = [
  { route: { name: 'ai-agents' }, label: 'global.nav.aiAgents', act: 'account.manage' },
  { route: { name: 'machines' }, label: 'global.nav.machines', act: 'view' },
]

/** The first section of Settings the account can use, else the Settings page. */
export function settingsHome(me: Me): Route {
  return settingsSections.find((s) => can(me, s.act))?.route ?? { name: 'settings' }
}

/** Whether a route is one of the Settings pages, for the sidebar's Settings row. */
export function inSettings(route: Route): boolean {
  return route.name === 'settings' || route.name === 'ai-agents' || route.name === 'machines' || route.name === 'machine-details'
}
