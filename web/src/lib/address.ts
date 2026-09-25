import type { Address, CertificateStatus, DNSRecord, JoinAddress, Operation } from '@/api/types'
import { t } from '@/i18n'

export const nameMin = 3
export const nameMax = 32
/** How many servers get an address under a free name, as the names service allows. */
export const freeServerMax = 5
const labelRe = /^[a-z0-9]+(-[a-z0-9]+)*$/

/** A typed name the way the agent reads it: trimmed, lowercase, without a trailing dot or the base domain. */
export function normalizeName(input: string, base: string): string {
  const s = input.trim().toLowerCase().replace(/\.$/, '')
  const suffix = `.${base.toLowerCase()}`
  return s.endsWith(suffix) ? s.slice(0, -suffix.length) : s
}

export type NameProblem = 'empty' | 'characters' | 'short' | 'long'

/** What's wrong with a free name under the names service's rule, if anything. */
export function nameProblem(name: string): NameProblem | undefined {
  if (!name) return 'empty'
  if (!labelRe.test(name)) return 'characters'
  if (name.length < nameMin) return 'short'
  if (name.length > nameMax) return 'long'
  return undefined
}

/** The servers that get an address under a free name: the first five whose slug can be a DNS label. */
export function freeServers<T extends { slug: string }>(servers: T[]): T[] {
  return servers.filter((s) => s.slug.length <= nameMax && labelRe.test(s.slug)).slice(0, freeServerMax)
}

/** The dashboard's address under a name. It keeps its port, which isn't 443. */
export function dashboardURL(host: string, port: number): string {
  return port === 443 ? `https://${host}` : `https://${host}:${port}`
}

const secondLevel = new Set(['ac', 'co', 'com', 'edu', 'gov', 'ltd', 'net', 'or', 'org', 'plc'])

/** Where people manage a domain's records: example.com for play.example.com, example.co.uk for play.example.co.uk. */
export function zoneOf(domain: string): string {
  const labels = domain.split('.')
  const tld = labels.at(-1) ?? ''
  const sld = labels.at(-2) ?? ''
  return labels.slice(labels.length >= 3 && tld.length === 2 && secondLevel.has(sld) ? -3 : -2).join('.')
}

export function runningOp(a: Address, kind?: string): Operation | undefined {
  const op = a.operation
  return op?.status === 'running' && (!kind || op.kind === kind) ? op : undefined
}

/** Whether the free name, and each server's address under it, is published. */
export function freePublished(a: Address): boolean {
  return a.free?.state === 'active' && a.free.dns === 'ok' && (a.servers ?? []).every((s) => !s.address || s.published)
}

export type FreeStage = 'claiming' | 'publishing' | 'lapsed' | 'done'

// A publish that starts this soon after the claim is the claim's own.
const claimWindowMs = 10 * 60_000

/**
 * Where a free address is: the claim's first steps, publishing (also after
 * Refresh), lapsed after a month offline, or done.
 */
export function freeStage(a: Address): FreeStage {
  const op = runningOp(a, 'address.publish')
  if (op && op.phase !== 'publishing' && a.free && Date.parse(op.startedAt) - Date.parse(a.free.claimedAt) < claimWindowMs) return 'claiming'
  if (op) return 'publishing'
  if (a.free?.state === 'lapsed') return 'lapsed'
  return freePublished(a) ? 'done' : 'publishing'
}

/** The claim step in progress: pointing the name at the machine (1) or getting the certificate (2). */
export function claimStep(op: Operation | undefined): 1 | 2 {
  return op?.phase === 'certificate' ? 2 : 1
}

export function certValid(c: CertificateStatus | undefined, now: number): boolean {
  return !!c?.notAfter && Date.parse(c.notAfter) > now
}

export type CertState = 'none' | 'getting' | 'active' | 'problem'

export function certState(a: Address, now: number): CertState {
  const op = runningOp(a)
  if (op && (op.kind === 'certificate.issue' || (op.kind === 'address.publish' && op.phase === 'certificate'))) return 'getting'
  if (a.certificate?.problem) return 'problem'
  return certValid(a.certificate, now) ? 'active' : 'none'
}

/** Whether an own domain works: its records check out and it has a certificate. */
export function ownDone(a: Address, now: number): boolean {
  return a.kind === 'own' && !!a.check?.ready && certValid(a.certificate, now)
}

/** Which servers a record serves, for the table's For column; short is the server's name alone. */
export function recordFor(r: DNSRecord, servers: JoinAddress[], short = false): string {
  if (r.type === 'SRV') {
    const s = servers.find((x) => x.serverId === r.serverId)
    const name = s?.name ?? r.name
    return short ? name : t('address.forServer', { server: name, port: r.srv?.port ?? s?.port ?? 0 })
  }
  const bare = servers.find((x) => x.port === 25565)
  return bare ? t('address.forDashboardAnd', { server: bare.name }) : t('address.dashboard')
}
