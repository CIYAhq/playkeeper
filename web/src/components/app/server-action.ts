import { post } from '@/api/client'
import type { ServerStatus } from '@/api/types'
import { errorText, serverApi } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'

/** Asks the panel to start, stop, restart or back up a server; a refusal shows as a toast. Outside the server page, so a page that only wakes a server doesn't load it. */
export async function serverAction(server: ServerStatus, action: 'start' | 'stop' | 'restart' | 'backups', body: unknown = {}): Promise<boolean> {
  try {
    await post(serverApi(server.id, `/${action}`), body)
    return true
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
    return false
  }
}
