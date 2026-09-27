import { Pip } from '@/components/app/art'
import { toastManager } from '@/components/ui/toast'
import { dt, type DemoKey } from './messages'
import { countDemoAction } from './welcome'

export type DemoAction = 'start' | 'stop' | 'restart' | 'backup' | 'restore' | 'version' | 'create' | 'delete' | 'download' | 'recoveryKey' | 'signOut'

const words: Record<DemoAction, [DemoKey, DemoKey]> = {
  start: ['demo.start', 'demo.startBody'],
  stop: ['demo.stop', 'demo.stopBody'],
  restart: ['demo.restart', 'demo.restartBody'],
  backup: ['demo.backup', 'demo.backupBody'],
  restore: ['demo.restore', 'demo.restoreBody'],
  version: ['demo.version', 'demo.versionBody'],
  create: ['demo.create', 'demo.createBody'],
  delete: ['demo.delete', 'demo.deleteBody'],
  download: ['demo.download', 'demo.downloadBody'],
  recoveryKey: ['demo.recoveryKey', 'demo.recoveryKeyBody'],
  signOut: ['demo.signOut', 'demo.signOutBody'],
}

/** How every action in the demo ends: "That was a demo restart · Nothing really restarted." */
export function demoToast(action: DemoAction) {
  const [title, body] = words[action]
  toastManager.add({
    title: (
      <span className="flex items-center gap-3" data-demo-toast={action}>
        <Pip pose="wave" size={36} />
        <span className="flex flex-col gap-0.5">
          <span>{dt(title)}</span>
          <span className="font-normal text-muted-foreground">{dt(body)}</span>
        </span>
      </span>
    ),
    timeout: 8000,
  })
  countDemoAction()
}
