// What the demo's make-believe panel does the same way wherever a write
// happens: a line in the recent activity, a row in the audit log, and the
// refusal while a server is busy.

import { ApiError } from '@/api/client'
import type { Activity, ActivityKind, ServerStatus } from '@/api/types'
import { opLabel } from '@/lib/phase'
import { demoUser, iso, machineId, type DemoState } from './data'
import { dt } from './messages'

/** A line in the recent activity, newest first. */
export function note(s: DemoState, at: number, serverId: string, kind: ActivityKind, more: Partial<Activity> = {}) {
  s.activity.unshift({ ts: iso(at), serverId, kind, ...more })
  s.activity.sort((a, b) => b.ts.localeCompare(a.ts))
  s.activity.splice(200)
}

/** A row in the audit log, by the demo's account. */
export function audit(s: DemoState, at: number, action: string, srv?: ServerStatus, target?: string, detail?: string) {
  s.audit.unshift({ id: (s.audit[0]?.id ?? 0) + 1, ts: iso(at), actor: demoUser, action, target: target ?? srv?.name, serverId: srv?.id, result: 'succeeded', detail, source: srv ? 'agent' : 'panel', machineId: srv ? machineId : undefined })
  s.audit.splice(200)
}

/** The refusal while a server is in the middle of something. */
export function busy(srv: ServerStatus): ApiError {
  return new ApiError(409, { error: dt('demo.busy', { what: srv.operation ? opLabel(srv.operation, srv) : srv.name }), code: 'busy' })
}
