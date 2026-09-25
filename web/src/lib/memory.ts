import type { ServerMemory } from '@/api/types'

export interface Segment {
  key: string
  label: string
  memoryMB: number
  kind: 'system' | 'running' | 'stopped' | 'new' | 'free'
}

/**
 * The memory bar of the new-server memory step: the system's reserve, each
 * other server's share (stopped servers keep theirs), the new server, and
 * what is left. Shares never add up to more than the machine has.
 */
export function memorySegments(totalMB: number, systemMB: number, servers: ServerMemory[], newMB: number): Segment[] {
  const out: Segment[] = [{ key: 'system', label: '', memoryMB: Math.min(systemMB, totalMB), kind: 'system' }]
  let used = out[0]?.memoryMB ?? 0
  for (const s of servers) {
    const mb = Math.max(0, Math.min(s.memoryMB, totalMB - used))
    out.push({ key: s.id, label: s.name, memoryMB: mb, kind: s.running ? 'running' : 'stopped' })
    used += mb
  }
  const mine = Math.max(0, Math.min(newMB, totalMB - used))
  out.push({ key: 'new', label: '', memoryMB: mine, kind: 'new' })
  used += mine
  out.push({ key: 'free', label: '', memoryMB: Math.max(0, totalMB - used), kind: 'free' })
  return out
}

/** The share of the bar a segment takes, in percent. */
export function share(seg: Segment, totalMB: number): number {
  return totalMB > 0 ? (seg.memoryMB / totalMB) * 100 : 0
}
