import type { PagePort, PublicPageView, PublicServer, PublicStream } from '@/api/types'
import { formatLocale, t } from '@/i18n'
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

/** Whether browsers reach a server's public page, for its Settings. The dashboard's machine serves every server's page, on its own ports. */
export type PageReach =
  | { kind: 'off' }
  | { kind: 'noAddress' }
  | { kind: 'live'; url: string }
  | { kind: 'plainOnly'; url: string; https: PagePort }
  | { kind: 'opening' }
  | { kind: 'waiting' }
  | { kind: 'blocked'; port: PagePort }

export function pageReach(v: PublicPageView): PageReach {
  if (!v.enabled) return { kind: 'off' }
  if (!v.host || !v.ports) return { kind: 'noAddress' }
  const { https, http } = v.ports
  if (https.state === 'open') return { kind: 'live', url: https.port === 443 ? `https://${v.host}` : `https://${v.host}:${https.port}` }
  if (http.state === 'open') return { kind: 'plainOnly', url: http.port === 80 ? `http://${v.host}` : `http://${v.host}:${http.port}`, https }
  if (https.state === 'waiting' || http.state === 'waiting') return { kind: 'waiting' }
  if (https.state === 'off' && http.state === 'off') return { kind: 'opening' }
  return { kind: 'blocked', port: https.state !== 'off' ? https : http }
}

/** Why a port can't take the page, in one line. */
export function portProblem(p: PagePort): string {
  switch (p.state) {
    case 'busy':
      return p.holder ? t('publicPage.busyBy', { holder: p.holder, port: p.port }) : t('publicPage.busy', { port: p.port })
    case 'claimed':
      return t('publicPage.claimed', { holder: p.holder ?? t('publicPage.anotherProgram'), port: p.port })
    case 'denied':
      return t('publicPage.denied', { port: p.port })
    case 'waiting':
      return t('publicPage.waiting')
    case 'open':
    case 'off':
      return ''
    default: {
      const unreachable: never = p.state
      return unreachable
    }
  }
}

/** The player a stream's card loads once someone asks to watch, on the page's own address. */
export function streamEmbed(stream: PublicStream, host: string): string {
  if (stream.site === 'twitch') return `https://player.twitch.tv/?channel=${encodeURIComponent(stream.channel)}&parent=${encodeURIComponent(host)}&autoplay=true`
  return `https://www.youtube-nocookie.com/embed/live_stream?channel=${encodeURIComponent(stream.channel)}&autoplay=1`
}

/** How long until a session starts: "2 days 4 h", "3 h 12 min", "12 min" or "a moment". */
export function untilText(next: string, now: number): string {
  const s = Math.floor((new Date(next).getTime() - now) / 1000)
  if (!Number.isFinite(s) || s < 60) return t('serverPage.aMoment')
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return t('serverPage.inDays', { count: d, h })
  if (h > 0) return t('serverPage.inHours', { h, m })
  return t('serverPage.inMinutes', { m })
}

/** When a session starts, in the visitor's own time: "Sat 3 Oct, 20:00". */
export function sessionTime(next: string): string {
  return new Date(next).toLocaleString(formatLocale(), { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }).replace(/\bSept\b/, 'Sep')
}
