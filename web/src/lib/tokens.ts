import type { AgentActivity, ApiToken, DialAddress, TokenRole } from '@/api/types'
import { t, type MessageKey } from '@/i18n'
import { formatList } from './format'

/** How many days a new token lasts; the panel's own default is the second. */
export const tokenDays = [30, 60, 90, 365] as const
export type TokenDays = (typeof tokenDays)[number]

/**
 * The address AI agents connect to: the dashboard's name when it has one,
 * which its certificate is for, else the address this page was opened with.
 */
export function mcpAddress(addresses: DialAddress[] | undefined, origin: string): string {
  const name = addresses?.find((a) => a.kind === 'name')
  return name ? `https://${name.address}/mcp` : `${origin}/mcp`
}

/** The MCP settings an AI tool needs, as JSON laid out the way the dashboard shows it. */
export function mcpSnippet(url: string, secret: string): string {
  return [
    '{',
    '  "mcpServers": {',
    '    "playkeeper": {',
    `      "url": ${JSON.stringify(url)},`,
    `      "headers": { "Authorization": ${JSON.stringify(`Bearer ${secret}`)} }`,
    '    }',
    '  }',
    '}',
  ].join('\n')
}

/** A secret cut short for showing where it would overflow; copying always takes the whole one. */
export function elideSecret(secret: string): string {
  return secret.length > 11 ? `${secret.slice(0, 11)}…` : secret
}

export function tokenRoleText(role: TokenRole): string {
  switch (role) {
    case 'viewer':
      return t('ai.role.viewer')
    case 'moderator':
      return t('ai.role.moderator')
    case 'admin':
      return t('ai.role.admin')
    default: {
      const unreachable: never = role
      return unreachable
    }
  }
}

export function tokenRoleHint(role: TokenRole): string {
  switch (role) {
    case 'viewer':
      return t('ai.role.viewerHint')
    case 'moderator':
      return t('ai.role.moderatorHint')
    case 'admin':
      return t('ai.role.adminHint')
    default: {
      const unreachable: never = role
      return unreachable
    }
  }
}

/** "All servers", "Survival", "Survival and Creative" or "5 servers"; servers since deleted count without a name. */
export function tokenServersText(token: Pick<ApiToken, 'allServers' | 'servers'>, servers: { id: string; name: string }[]): string {
  if (token.allServers) return t('ai.allServers')
  const names = token.servers.map((id) => servers.find((s) => s.id === id)?.name).filter((n): n is string => !!n)
  if (names.length < token.servers.length || names.length > 3) return t('ai.serverCount', { count: token.servers.length })
  return formatList(names)
}

const day = 86_400_000

/** "in 58 days", "today", or "ran out" once it has. */
export function runsOutText(expiresAt: string, now: number = Date.now()): string {
  const left = new Date(expiresAt).getTime() - now
  if (left <= 0) return t('ai.ranOut')
  const days = Math.floor(left / day)
  return days === 0 ? t('ai.runsOutToday') : t('ai.runsOutIn', { count: days })
}

export function tokenExpired(token: Pick<ApiToken, 'expiresAt'>, now: number = Date.now()): boolean {
  return new Date(token.expiresAt).getTime() <= now
}

const toolPhrases: Record<string, MessageKey> = {
  get_server_status: 'ai.did.get_server_status',
  start_server: 'ai.did.start_server',
  stop_server: 'ai.did.stop_server',
  restart_server: 'ai.did.restart_server',
  read_console: 'ai.did.read_console',
  send_chat_message: 'ai.did.send_chat_message',
  run_console_command: 'ai.did.run_console_command',
  list_online_players: 'ai.did.list_online_players',
  list_whitelist: 'ai.did.list_whitelist',
  add_to_whitelist: 'ai.did.add_to_whitelist',
  remove_from_whitelist: 'ai.did.remove_from_whitelist',
  list_backups: 'ai.did.list_backups',
  create_backup: 'ai.did.create_backup',
  get_lag_report: 'ai.did.get_lag_report',
  explain_crash: 'ai.did.explain_crash',
  search_addons: 'ai.did.search_addons',
  install_addon: 'ai.did.install_addon',
  remove_addon: 'ai.did.remove_addon',
}

/** What an agent did, in lower case after its name: "made a backup of Survival". */
export function agentPhrase(a: Pick<AgentActivity, 'tool' | 'serverName'>): string {
  const key = Object.hasOwn(toolPhrases, a.tool) ? toolPhrases[a.tool] : undefined
  const server = a.serverName ?? ''
  if (key && server) return t(key, { server })
  return server ? t('ai.did.other', { tool: a.tool, server }) : t('ai.did.otherPlain', { tool: a.tool })
}
