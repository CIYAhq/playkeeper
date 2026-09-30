// The live demo's AI keys, which AI Build Battle builds with. A key is
// checked the way the agent checks it and then dropped: the demo keeps only
// which providers have one, so nothing a visitor pastes is ever saved.

import { ApiError } from '@/api/client'
import type { AIKey, AIKeys, AIProvider, ServerStatus } from '@/api/types'
import { t } from '@/i18n'
import { aiKeyMessage, aiKeyProblem, aiProviders, isAIBuildBattle } from '@/lib/ai-keys'
import { serverOf, type DemoState, type Request, type Routes } from './data'
import { audit, busy } from './shared'

function providerOf(r: Request): AIProvider {
  const p = r.params.provider ?? ''
  if (!Object.hasOwn(aiProviders, p)) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  return p as AIProvider
}

function keysOf(s: DemoState, srv: ServerStatus): AIKeys {
  const saved = s.aiKeys?.[srv.id] ?? []
  const keys = Object.fromEntries(Object.keys(aiProviders).map((p) => [p, { set: saved.includes(p as AIProvider) }])) as Record<AIProvider, AIKey>
  return { keys, pending: false, available: (s.addons[srv.id]?.installed ?? []).some(isAIBuildBattle) }
}

function change(s: DemoState, r: Request, save: boolean): AIKeys {
  const srv = serverOf(s, r)
  const provider = providerOf(r)
  if (save) {
    const reason = aiKeyProblem(provider, String((r.body as { key?: unknown } | undefined)?.key ?? ''))
    if (reason) throw new ApiError(400, { error: aiKeyMessage(provider, reason), code: 'invalid_request', field: 'key', reason })
  }
  if (srv.operation) throw busy(srv)
  const saved = (s.aiKeys ??= {})[srv.id] ?? []
  const had = saved.includes(provider)
  s.aiKeys[srv.id] = save ? [...saved.filter((p) => p !== provider), provider] : saved.filter((p) => p !== provider)
  if (save || had) audit(s, r.now, save ? 'ai_key.saved' : 'ai_key.removed', srv, provider)
  return keysOf(s, srv)
}

export const aiKeyRoutes: Routes = {
  'GET /api/servers/:id/ai-keys': (s, r) => keysOf(s, serverOf(s, r)),
  'PUT /api/servers/:id/ai-keys/:provider': (s, r) => change(s, r, true),
  'DELETE /api/servers/:id/ai-keys/:provider': (s, r) => change(s, r, false),
}
