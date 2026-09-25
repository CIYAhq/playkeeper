import type { Operation, Phase, ServerStatus } from '@/api/types'
import { t, type MessageKey } from '@/i18n'

export type Tone = 'online' | 'busy' | 'stopped' | 'crashed' | 'unknown'

export function phaseTone(p: Phase): Tone {
  switch (p) {
    case 'online':
      return 'online'
    case 'pulling_image':
    case 'starting_container':
    case 'downloading_server':
    case 'starting':
    case 'preparing_world':
    case 'stopping':
      return 'busy'
    case 'stopped':
    case 'not_created':
    case 'asleep':
      return 'stopped'
    case 'crashed':
      return 'crashed'
    case 'docker_unavailable':
      return 'unknown'
    default: {
      const unreachable: never = p
      return unreachable
    }
  }
}

export function phaseLabel(p: Phase): string {
  switch (p) {
    case 'online':
      return t('status.online')
    case 'stopped':
      return t('status.stopped')
    case 'crashed':
      return t('status.crashed')
    case 'pulling_image':
    case 'downloading_server':
      return t('status.downloading')
    case 'starting_container':
    case 'starting':
      return t('status.starting')
    case 'preparing_world':
      return t('status.preparing')
    case 'stopping':
      return t('status.stopping')
    case 'not_created':
      return t('status.notCreated')
    case 'docker_unavailable':
      return t('status.docker')
    case 'asleep':
      return t('status.asleep')
    default: {
      const unreachable: never = p
      return unreachable
    }
  }
}

/** Is the server being set up for the first time (its create is running or failed)? */
export function isSettingUp(st: ServerStatus): boolean {
  const op = st.operation ?? st.lastOperation
  if (st.operation?.kind === 'create') return true
  return !!op && op.kind === 'create' && op.status === 'failed' && !st.startedAt && st.phase !== 'online'
}

/** Which lifecycle controls make sense in the current state. */
export function controls(st: ServerStatus) {
  const busy = st.operation !== undefined
  const running = ['online', 'starting', 'starting_container', 'preparing_world', 'downloading_server', 'stopping'].includes(st.phase)
  const dockerDown = st.phase === 'docker_unavailable'
  return {
    canStart: st.exists && !busy && !dockerDown && !running,
    // Stopping a sleeping server keeps it off: nobody's join wakes it then.
    canStop: st.exists && !busy && !dockerDown && ((running && st.phase !== 'stopping') || st.phase === 'asleep'),
    canRestart: st.exists && !busy && !dockerDown && st.phase === 'online',
    busy,
  }
}

const opKeys: Record<string, MessageKey> = {
  create: 'op.create',
  start: 'op.start',
  stop: 'op.stop',
  restart: 'op.restart',
  backup: 'op.backup',
  restore: 'op.restore',
  recover: 'op.recover',
  'auto-restart': 'op.auto-restart',
  'update-version': 'op.update-version',
  delete: 'op.delete',
  update: 'op.update',
  // Wave 7
  sleep: 'op.sleep',
  wake: 'op.wake',
  'disk-cleanup': 'op.disk-cleanup',
  'offsite-restore': 'op.offsite-restore',
}

/** "Backing up Survival", for the job pill and busy notes. */
export function opLabel(op: Operation, server: string): string {
  return t(opKeys[op.kind] ?? 'op.other', { server })
}

/**
 * Which of the setup steps (checked, downloaded, starting, reachable) a
 * server's phase is in.
 */
export function createStepOf(phase: string): number {
  switch (phase) {
    case '':
      return 0
    case 'pulling_image':
    case 'downloading_server':
    case 'verifying_download':
      return 1
    case 'starting_container':
    case 'starting':
    case 'preparing_world':
      return 2
    case 'online':
      return 3
  }
  return 0
}
