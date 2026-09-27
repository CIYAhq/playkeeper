import type { Action, Me, ProjectRole, Scope, TokenRole } from '@/api/types'
import { t, type MessageKey } from '@/i18n'
import { formatList } from './format'
import type { Route } from './router'

/** Whether the signed-in account may take act. The panel checks every request anyway. */
export function can(me: Me, act: Action): boolean {
  return me.access.can.includes(act)
}

export const projectRoles: ProjectRole[] = ['admin', 'moderator', 'viewer']

/** The pref the invite page sets when it makes an account, so Home greets the new member once. */
export const welcomeKey = 'home.welcome'

export function roleName(role: ProjectRole): string {
  switch (role) {
    case 'admin':
      return t('role.admin')
    case 'moderator':
      return t('role.moderator')
    case 'viewer':
      return t('role.viewer')
    default: {
      const unreachable: never = role
      return unreachable
    }
  }
}

/** What a role lets someone do, in one line. */
export function roleHint(role: ProjectRole): string {
  switch (role) {
    case 'admin':
      return t('role.adminHint')
    case 'moderator':
      return t('role.moderatorHint')
    case 'viewer':
      return t('role.viewerHint')
    default: {
      const unreachable: never = role
      return unreachable
    }
  }
}

/** The servers a scope covers, by name: "All servers", "Survival only", "Survival and Creative". */
export function scopeText(scope: Scope, servers: { id: string; name: string }[]): string {
  if (scope.all) return t('scope.all')
  const ids = scope.servers ?? []
  const names = servers.filter((s) => ids.includes(s.id)).map((s) => s.name)
  if (ids.length === 0) return t('scope.none')
  if (names.length < ids.length || ids.length > 3) return t('scope.count', { count: ids.length })
  if (names.length === 1) return t('scope.only', { server: names[0] ?? '' })
  return formatList(names)
}

const tokenRoleOrder: TokenRole[] = ['viewer', 'moderator', 'admin']

/** The roles a token of this account may have: never more than the account's own role. */
export function tokenRoles(me: Me): TokenRole[] {
  return tokenRoleOrder.slice(0, tokenRoleOrder.indexOf(me.access.role) + 1)
}

export type SettingsSectionName = 'team' | 'addon-sources' | 'discord' | 'ai-agents' | 'machines'

/** The sections of Settings in the design's order, each for the accounts that may use it. */
export const settingsSections: { route: Route & { name: SettingsSectionName }; label: MessageKey; act: Action }[] = [
  { route: { name: 'team' }, label: 'global.nav.team', act: 'team.manage' },
  { route: { name: 'addon-sources' }, label: 'global.nav.addonSources', act: 'machine.manage' },
  { route: { name: 'discord' }, label: 'global.nav.discord', act: 'machine.manage' },
  { route: { name: 'ai-agents' }, label: 'global.nav.aiAgents', act: 'account.manage' },
  { route: { name: 'machines' }, label: 'global.nav.machines', act: 'view' },
]

/** Where Settings opens: the first section the account can use, else the general page. */
export function settingsHome(me: Me): Route {
  return settingsSections.find((s) => can(me, s.act))?.route ?? { name: 'settings' }
}

/** Whether a route is one of the Settings pages, for the sidebar's Settings row. */
export function inSettings(route: Route): boolean {
  return route.name === 'settings' || route.name === 'machine-details' || settingsSections.some((s) => s.route.name === route.name)
}
