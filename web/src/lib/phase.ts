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

/** A server that is stopped with a crash or a refused start to explain looks crashed. */
export function statusTone(st: ServerStatus): Tone {
  const tone = phaseTone(st.phase)
  return tone === 'stopped' && (st.crash || st.refusal) ? 'crashed' : tone
}

/** Did the server's last start fail before it came up? */
export function couldntStart(st: ServerStatus): boolean {
  return !!st.refusal || !!st.crash?.start || !!st.softwareChanged
}

/** "Crashed", "Couldn't start" when it never came up, or the phase. */
export function statusLabel(st: ServerStatus): string {
  if (statusTone(st) !== 'crashed') return phaseLabel(st.phase)
  return couldntStart(st) ? t('status.couldntStart') : t('status.crashed')
}

/** Is the server being set up for the first time (its create is running or failed)? */
export function isSettingUp(st: ServerStatus): boolean {
  const op = st.operation ?? st.lastOperation
  if (st.operation?.kind === 'create') return true
  return !!op && op.kind === 'create' && op.status === 'failed' && !st.startedAt && st.phase !== 'online'
}

/** Is the server's create running right now? A create that failed isn't: the server is stopped. */
export function isCreating(st: ServerStatus): boolean {
  return st.operation?.kind === 'create'
}

/** Which lifecycle controls make sense in the current state. */
export function controls(st: ServerStatus) {
  const busy = st.operation !== undefined
  const running = ['online', 'starting', 'starting_container', 'preparing_world', 'downloading_server', 'stopping'].includes(st.phase)
  const dockerDown = st.phase === 'docker_unavailable'
  return {
    canStart: st.exists && !busy && !dockerDown && !running && !st.softwareChanged,
    // Stopping a sleeping server keeps it off: nobody's join wakes it then.
    canStop: st.exists && !busy && !dockerDown && ((running && st.phase !== 'stopping') || st.phase === 'asleep'),
    canRestart: st.exists && !busy && !dockerDown && st.phase === 'online',
    busy,
  }
}

/** "Backing up Survival. Try again when it's done." while a job runs; undefined otherwise. */
export function busyReason(st: ServerStatus): string | undefined {
  return st.operation ? t('reason.busy', { what: opLabel(st.operation, st.name) }) : undefined
}

export type ServerAction = 'start' | 'stop' | 'restart' | 'command' | 'change' | 'backup'

/** Why an action can't run on a server right now, in a few plain words; undefined when it can. */
export function whyNot(st: ServerStatus, action: ServerAction, stale: boolean): string | undefined {
  if (stale) return t('reason.noAgent')
  if (st.phase === 'docker_unavailable') return t('status.docker')
  if (!st.exists) return t('reason.notCreated', { server: st.name })
  const busy = busyReason(st)
  if (busy) return busy
  const c = controls(st)
  const settling = phaseTone(st.phase) === 'busy' ? t('reason.busy', { what: t(st.phase === 'stopping' ? 'op.stop' : 'op.start', { server: st.name }) }) : undefined
  switch (action) {
    case 'start':
      if (st.softwareChanged) return t('reason.softwareChanged')
      if (st.worldMissing) return t('reason.worldMissing')
      return c.canStart ? undefined : (settling ?? t('reason.running', { server: st.name }))
    case 'stop':
      return c.canStop ? undefined : (settling ?? t('reason.stopped', { server: st.name }))
    case 'restart':
    case 'command':
      return st.phase === 'online' ? undefined : (settling ?? t('reason.startFirst', { server: st.name }))
    case 'change':
      return undefined
    case 'backup':
      return st.worldMissing ? t('reason.worldMissing') : undefined
    default: {
      const unreachable: never = action
      return unreachable
    }
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
  'remove-addon': 'op.remove-addon',
  // Wave 4.
  reinstall: 'op.reinstall',
  'template-retry': 'op.templateRetry',
  // Wave 7
  sleep: 'op.sleep',
  wake: 'op.wake',
  'disk-cleanup': 'op.disk-cleanup',
  'offsite-restore': 'op.offsite-restore',
  'offsite-check': 'op.offsite-check',
}

/** "Backing up Survival", for the job pill and busy notes. */
export function opLabel(op: Operation, server: string): string {
  return t(opKeys[op.kind] ?? 'op.other', { server })
}

const recentMs = 15 * 60_000

/** Whether what a failed job wanted has happened since, so its notice can go. */
function recovered(s: ServerStatus, op: Operation): boolean {
  // Refused because a restore left the world folder missing: its own notice
  // says so until the world is back, and then the refusal is over.
  if (op.detail?.errorKind === 'world_missing') return !s.worldMissing
  if (['create', 'start', 'restart', 'recover', 'auto-restart'].includes(op.kind)) return s.phase === 'online'
  if (op.kind !== 'backup') return false
  const needed = op.detail?.neededBytes
  if (typeof needed === 'number') return (s.resources?.diskFreeBytes ?? 0) >= needed
  return op.detail?.errorKind === 'saving_paused' && !s.savingPausedSince
}

/** The last job, if it failed in the last 15 minutes and nothing has put it right since. */
export function failedJob(s: ServerStatus, now = Date.now()): Operation | undefined {
  const op = s.lastOperation
  if (!op || op.status !== 'failed' || !op.finishedAt || now - new Date(op.finishedAt).getTime() >= recentMs) return undefined
  return recovered(s, op) ? undefined : op
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

/**
 * The setup steps of a server made from a modpack: checked, the server
 * software, the pack's files, starting, reachable.
 */
export function packStepOf(phase: string): number {
  switch (phase) {
    case 'preparing_modpack':
      return 1
    case 'installing_modpack':
    case 'installing_addons':
      return 2
  }
  const at = createStepOf(phase)
  return at >= 2 ? at + 1 : at
}

/**
 * The setup steps of a server made from a template with add-ons: checked,
 * the server software, the add-ons, starting, reachable.
 */
export function templateStepOf(phase: string): number {
  if (phase === 'installing_addons') return 2
  const at = createStepOf(phase)
  return at >= 2 ? at + 1 : at
}
