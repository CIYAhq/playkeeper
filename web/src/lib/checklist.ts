import type { ServerStatus } from '@/api/types'

export type StepId = 'create' | 'invite' | 'joined' | 'backup' | 'download'
export type StepState = 'done' | 'next' | 'todo' | 'locked'

export interface Step {
  id: StepId
  state: StepState
}

/**
 * The "Get started" steps. With no server, creating one is the first of
 * five; after that each server has its own four. Downloading a backup
 * unlocks once there is one, and the first step not done is "next".
 */
export function checklist(server: ServerStatus | undefined): Step[] {
  const fs = server?.firstSteps
  const done: Record<StepId, boolean> = {
    create: !!server,
    invite: !!fs?.invited,
    joined: !!fs?.friendJoined,
    backup: !!fs?.backedUp,
    download: !!fs?.downloaded,
  }
  const ids: StepId[] = server ? ['invite', 'joined', 'backup', 'download'] : ['create', 'invite', 'joined', 'backup', 'download']
  let nextGiven = false
  return ids.map((id) => {
    if (done[id]) return { id, state: 'done' as const }
    if (id === 'download' && !done.backup) return { id, state: 'locked' as const }
    // A friend joining ticks itself, so it is never the step to act on.
    if (!nextGiven && id !== 'joined') {
      nextGiven = true
      return { id, state: 'next' as const }
    }
    return { id, state: 'todo' as const }
  })
}

export function progress(steps: Step[]): { done: number; total: number; next?: Step } {
  return { done: steps.filter((s) => s.state === 'done').length, total: steps.length, next: steps.find((s) => s.state === 'next') }
}

export function complete(steps: Step[]): boolean {
  return steps.every((s) => s.state === 'done')
}
