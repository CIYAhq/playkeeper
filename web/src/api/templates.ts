import { useEffect, useState } from 'react'
import { api, get } from './client'
import type { TemplateExport, TemplatePlan } from './types'
import { errorText, machineApi, serverApi } from './workspace'

export interface TemplateOptions {
  addons: boolean
  settings: boolean
  packs: boolean
  latest: boolean
}

export function templateQuery(o: TemplateOptions): string {
  const q = new URLSearchParams()
  if (!o.addons) q.set('addons', 'off')
  if (!o.settings) q.set('settings', 'off')
  if (!o.packs) q.set('packs', 'off')
  if (o.latest) q.set('versions', 'latest')
  const s = q.toString()
  return s ? `?${s}` : ''
}

/** The server's setup as a template with the dialog's choices. The last export stays while the next loads. */
export function useTemplateExport(serverId: string, options: TemplateOptions, open: boolean) {
  const path = open ? serverApi(serverId, `/template${templateQuery(options)}`) : ''
  const [state, setState] = useState<{ serverId: string; data?: TemplateExport; error?: string }>({ serverId })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!path) return
    let cancelled = false
    setState((s) => (s.serverId === serverId ? { serverId, data: s.data } : { serverId }))
    get<TemplateExport>(path)
      .then((data) => !cancelled && setState({ serverId, data }))
      .catch((e: unknown) => !cancelled && setState((s) => ({ ...s, error: errorText(e) })))
    return () => {
      cancelled = true
    }
  }, [path, serverId, tick])
  const current = state.serverId === serverId ? state : { data: undefined, error: undefined }
  return { data: current.data, error: current.error, loading: !!path && !current.data && !current.error, reload: () => setTick((n) => n + 1) }
}

/** Reads a template file, link or link data on the machine and plans the server it makes. */
export function planTemplate(machineId: string, text: string): Promise<TemplatePlan> {
  return api<TemplatePlan>('POST', machineApi(machineId, '/templates/plan'), undefined, new Blob([text], { type: 'text/plain' }))
}
