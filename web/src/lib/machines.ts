import type { LinkProblem, Machine, MachineEvent, MachineView, ServerStatus } from '@/api/types'
import { t } from '@/i18n'
import { formatMB, formatSpan } from './format'

/** A machine's name: the one it joined with, else its host name. */
export function machineLabel(m: MachineView | undefined): string {
  return m?.name || m?.link?.name || m?.live?.hostname || ''
}

/** The machine a server runs on: the one the dashboard has for it, else the dashboard's own. */
export function machineOf(s: Pick<ServerStatus, 'machineId'> | undefined, machines: MachineView[]): MachineView | undefined {
  return (s?.machineId ? machines.find((m) => m.id === s.machineId) : undefined) ?? machines.find((m) => m.kind === 'local')
}

/** Servers by the machine they run on, in the machines' order; a machine without servers gets an empty group. */
export function byMachine<S extends Pick<ServerStatus, 'machineId'>>(servers: S[], machines: MachineView[]): { machine: MachineView; servers: S[] }[] {
  const groups = machines.map((machine) => ({ machine, servers: [] as S[] }))
  for (const s of servers) groups.find((g) => g.machine === machineOf(s, machines))?.servers.push(s)
  return groups
}

export type MachineTone = 'good' | 'warn' | 'off'

/**
 * How a machine is doing, in a word. The dashboard's own machine goes by its
 * agent; a joined one by its link first, then its agent.
 */
export function machineState(m: MachineView, own: { agentDown: boolean; updating?: string }): { tone: MachineTone; label: string } {
  if (m.kind === 'local') {
    if (own.updating || (!own.agentDown && m.live?.docker)) return { tone: 'good', label: t('nav.healthy') }
    return { tone: 'warn', label: own.agentDown || !m.live ? t('nav.notAnswering') : t('status.docker') }
  }
  const state = m.link?.state
  switch (state) {
    case 'connected':
      if (m.live?.updateInstalling) return { tone: 'good', label: t('nav.updating') }
      if (m.error || !m.live) return { tone: 'warn', label: t('nav.notAnswering') }
      return m.live.docker ? { tone: 'good', label: t('nav.healthy') } : { tone: 'warn', label: t('status.docker') }
    case 'waiting':
      return { tone: 'off', label: t('machines.waiting') }
    case 'offline':
    case 'removed':
    case undefined:
      return { tone: 'off', label: t('machines.offline') }
    default: {
      const unreachable: never = state
      return unreachable
    }
  }
}

/** Whether the dashboard can't reach a joined machine right now; its own machine never counts. */
export function isAway(m: MachineView | undefined): boolean {
  return m?.kind === 'remote' && m.link?.state !== 'connected'
}

/** Whether a machine's agent doesn't answer although the machine itself can be reached. */
export function agentDownOn(m: MachineView | undefined, ownAgentDown: boolean): boolean {
  if (!m) return ownAgentDown
  if (m.kind === 'local') return ownAgentDown
  return m.link?.state === 'connected' && m.error?.code === 'agent_unavailable'
}

/**
 * Whether a server's status is live, or why not: its machine is away, or
 * that machine's agent doesn't answer. since is when it was last heard.
 */
export type Reach = { state: 'live' } | { state: 'away'; machine: MachineView; since?: string } | { state: 'agentDown'; machine: MachineView | undefined; since?: string }

export function reachOf(s: ServerStatus, ws: { machines: MachineView[]; agentDown: boolean; lastSeenAt?: number }): Reach {
  const m = machineOf(s, ws.machines)
  if (m && isAway(m)) return { state: 'away', machine: m, since: m.link?.lastSeen ?? s.lastKnownAt }
  if (agentDownOn(m, ws.agentDown)) {
    const since = m?.kind === 'remote' ? s.lastKnownAt : ws.lastSeenAt ? new Date(ws.lastSeenAt).toISOString() : undefined
    return { state: 'agentDown', machine: m, since }
  }
  return { state: 'live' }
}

/** Whether what the dashboard shows about a server is the last it heard rather than live. */
export function isStale(s: ServerStatus, stale: boolean): boolean {
  return stale || !!s.lastKnownAt
}

/** "Ubuntu 24.04 · 4 vCPU · 16 GB", then any other parts given. */
export function systemLine(live: Machine | undefined, ...extra: string[]): string {
  const parts = live ? [live.os, t('machines.vcpu', { count: live.cpus }), formatMB(live.memoryTotalMB)] : []
  return [...parts, ...extra.filter(Boolean)].join(t('common.dot'))
}

/** A fingerprint in groups of four, as playkeeper status prints it. */
export function groupFingerprint(fp: string): string {
  return (fp.match(/.{1,4}/g) ?? []).join(' ')
}

/**
 * The host players dial for a server: a joined machine's address as the
 * dashboard last saw it, else the address the dashboard was opened with.
 */
export function joinHost(m: MachineView | undefined, dashboardHost: string): string {
  const addr = m?.kind === 'remote' ? m.link?.address : undefined
  if (!addr) return dashboardHost
  const v6 = /^\[([^\]]+)\]/.exec(addr)
  if (v6?.[1]) return v6[1]
  const parts = addr.split(':')
  return parts.length === 2 && parts[0] ? parts[0] : addr
}

/** "10 min", "3 h" or "2 days" since a moment, for a status pill. */
export function awayShort(since: string, now: number): string {
  const s = Math.max(0, (now - new Date(since).getTime()) / 1000)
  if (s < 3600) return t('machines.awayMin', { count: Math.max(1, Math.floor(s / 60)) })
  if (s < 86400) return t('machines.awayHours', { count: Math.floor(s / 3600) })
  return t('time.duration.days', { count: Math.floor(s / 86400) })
}

/** "10 minutes" since a moment. */
export function awayLong(since: string, now: number): string {
  return formatSpan((now - new Date(since).getTime()) / 1000)
}

/** Seconds as a countdown: "29:12", or "1:02:05" past an hour. */
export function countdown(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const pad = (n: number) => String(n).padStart(2, '0')
  return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`
}

/** A link problem's headline and what to do, in the dashboard's words. */
export function problemText(p: LinkProblem, now: number): { title: string; hint?: string } {
  const name = p.params?.name ?? ''
  const since = p.params?.since
  switch (p.code) {
    case 'machine_offline':
      return { title: t('machines.problem.offline', { name, duration: since ? awayLong(since, now) : (p.params?.duration ?? '') }), hint: t('machines.problem.offlineHint', { name }) }
    case 'machine_never_connected':
      return { title: t('machines.problem.neverConnected', { name, duration: p.params?.duration ?? '' }), hint: t('machines.problem.neverConnectedHint') }
    case 'link_slow':
      return { title: t('machines.problem.slow', { name, seconds: (Number(p.params?.rttMs ?? 0) / 1000).toFixed(1) }), hint: t('machines.problem.slowHint') }
    case 'clock_skew':
      return {
        title: t(p.params?.direction === 'behind' ? 'machines.problem.clockBehind' : 'machines.problem.clockAhead', { name, duration: formatSpan(Number(p.params?.seconds ?? 0)) }),
        hint: t('machines.problem.clockHint'),
      }
    case 'version_mismatch':
      return {
        title: t('machines.problem.version', { name, version: p.params?.version ?? '', dashboardVersion: p.params?.dashboardVersion ?? '' }),
        hint: p.params?.older === 'machine' ? t('machines.problem.versionHint') : t('machines.problem.versionDashboardHint'),
      }
    case 'link_unstable':
      return { title: t('machines.problem.unstable', { name, count: p.params?.count ?? '' }), hint: t('machines.problem.unstableHint') }
    case 'machine_cloned':
      return { title: t('machines.problem.cloned', { name }), hint: t('machines.problem.clonedHint', { name }) }
    default: {
      const unknown: never = p.code
      return { title: String(unknown) }
    }
  }
}

/** A joined machine that runs an older Playkeeper than the dashboard, which the dashboard can update. */
export function olderMachine(p: LinkProblem | undefined): boolean {
  return p?.code === 'version_mismatch' && p.params?.older === 'machine'
}

/**
 * What a machine event says. Events come newest first, so a lost connection
 * can say how long it lasted when a newer event is the reconnection.
 */
export function machineEventText(events: MachineEvent[], i: number): string {
  const e = events[i]
  if (!e) return ''
  const dot = t('common.dot')
  switch (e.kind) {
    case 'machine.joined': {
      const who = e.actor ? t('machines.event.joinedBy', { actor: e.actor }) : t('machines.event.joined')
      return e.address ? `${who}${dot}${t('machines.event.from', { address: e.address })}` : who
    }
    case 'machine.connected':
      return events[i + 1]?.kind === 'machine.disconnected' ? t('machines.event.reconnected') : t('machines.event.connected')
    case 'machine.disconnected': {
      const back = events[i - 1]
      const lost = back?.kind === 'machine.connected' ? t('machines.event.lostFor', { duration: formatSpan((new Date(back.at).getTime() - new Date(e.at).getTime()) / 1000) }) : t('machines.event.lost')
      const why = e.code === 'link_heartbeat_timeout' ? t('machines.event.whyQuiet') : e.code === 'link_dropped' ? t('machines.event.whyDropped') : ''
      return why ? `${lost}${dot}${why}` : lost
    }
    case 'machine.update':
      return e.actor ? t('machines.event.updateBy', { actor: e.actor }) : t('machines.event.update')
    case 'machine.removed':
      return e.actor ? t('machines.event.removedBy', { actor: e.actor }) : t('machines.event.removed')
    case 'machine.left':
      return t('machines.event.left')
    case 'machine.server_disputed':
      return t('machines.event.disputed')
    default:
      return e.kind
  }
}
