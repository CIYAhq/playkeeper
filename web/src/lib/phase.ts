import type { Phase, ServerStatus } from '../api/types'

export type Tone = 'good' | 'busy' | 'idle' | 'bad' | 'warn'

export const phaseLabel: Record<Phase, string> = {
  not_created: 'Not created',
  stopped: 'Stopped',
  pulling_image: 'Downloading runtime',
  starting_container: 'Starting',
  downloading_server: 'Downloading Paper',
  starting: 'Starting',
  preparing_world: 'Preparing world',
  online: 'Online',
  stopping: 'Stopping',
  crashed: 'Crashed',
  docker_unavailable: 'Docker unavailable',
}

export function phaseTone(p: Phase): Tone {
  switch (p) {
    case 'online':
      return 'good'
    case 'pulling_image':
    case 'starting_container':
    case 'downloading_server':
    case 'starting':
    case 'preparing_world':
    case 'stopping':
      return 'busy'
    case 'stopped':
    case 'not_created':
      return 'idle'
    case 'crashed':
      return 'bad'
    case 'docker_unavailable':
      return 'warn'
    default: {
      const unreachable: never = p
      return unreachable
    }
  }
}

/** Which lifecycle controls make sense in the current state. */
export function controls(st: ServerStatus) {
  const busy = st.operation !== undefined
  const running = ['online', 'starting', 'starting_container', 'preparing_world', 'downloading_server', 'stopping'].includes(st.phase)
  const dockerDown = st.phase === 'docker_unavailable'
  return {
    canStart: st.exists && !busy && !dockerDown && !running,
    canStop: st.exists && !busy && !dockerDown && running && st.phase !== 'stopping',
    canRestart: st.exists && !busy && !dockerDown && st.phase === 'online',
    busy,
  }
}

/** The ordered steps shown while a server is being created or started. */
export const startSteps: { phases: string[]; label: string }[] = [
  { phases: ['pulling_image'], label: 'Download the Minecraft runtime image' },
  { phases: ['downloading_server', 'verifying_download'], label: 'Download Paper and verify its checksum' },
  { phases: ['starting_container', 'starting'], label: 'Start the server' },
  { phases: ['preparing_world'], label: 'Prepare the world' },
  { phases: ['online'], label: 'Online and reachable from this server' },
]

export function stepIndex(phase: string): number {
  return startSteps.findIndex((s) => s.phases.includes(phase))
}
