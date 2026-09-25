import type { Action, Me, ProjectRole, Scope } from '@/api/types'
import { t } from '@/i18n'
import { formatList } from './format'

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
  const names = ids.map((id) => servers.find((s) => s.id === id)?.name).filter((n): n is string => !!n)
  if (ids.length === 0) return t('scope.none')
  if (names.length < ids.length || ids.length > 3) return t('scope.count', { count: ids.length })
  if (names.length === 1) return t('scope.only', { server: names[0] ?? '' })
  return formatList(names)
}
