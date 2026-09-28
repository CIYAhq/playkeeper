import type { PublicServer } from '@/api/types'
import { t } from '@/i18n'
import type { Tone } from '@/lib/phase'

/** The public page's data, a server's icon and a player's face, on the machine's address. */
export const serverPageApi = '/api/public/server-page'

export function serverPageIcon(s: PublicServer): string | undefined {
  return s.hasIcon ? `${serverPageApi}/icons/${encodeURIComponent(s.slug)}` : undefined
}

export function serverPageFace(name: string): string {
  return `${serverPageApi}/faces/${encodeURIComponent(name)}`
}

/** A server's status on the public page: the dot, its word and a short detail. */
export function publicStatus(s: PublicServer): { tone: Tone; label: string; detail?: string } {
  switch (s.state) {
    case 'online': {
      const p = s.players
      const detail = !p ? undefined : p.online > 0 ? t('serverPage.playing', { count: p.online, max: p.max }) : t('serverPage.nobody')
      return { tone: 'online', label: t('serverPage.online'), detail }
    }
    case 'starting':
      return { tone: 'busy', label: t('serverPage.starting'), detail: t('serverPage.startingDetail') }
    case 'sleeping':
      return { tone: 'stopped', label: t('serverPage.sleeping'), detail: t('serverPage.sleepingDetail') }
    case 'offline':
      return { tone: 'stopped', label: t('serverPage.offline') }
    default: {
      const unreachable: never = s.state
      return unreachable
    }
  }
}

/** The two lines on how to join, and a third when only invited players get in. */
export function joinSteps(s: PublicServer): string[] {
  const steps = [s.modpack ? t('serverPage.stepPack', { pack: s.modpack.name }) : t('serverPage.step', { version: s.minecraftVersion }), t('serverPage.stepPaste')]
  if (s.inviteOnly) steps.push(t('serverPage.inviteOnly'))
  return steps
}

/** The browser tab's title: the server's name, or the address for a machine with several. */
export function serverPageTitle(address: string, servers: PublicServer[]): string {
  const only = servers.length === 1 ? servers[0] : undefined
  return only ? t('serverPage.title', { server: only.name }) : t('serverPage.titleMany', { address })
}
