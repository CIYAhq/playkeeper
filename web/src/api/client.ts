import { t } from '@/i18n'
import type { ApiErrorBody, Operation } from './types'

export class ApiError extends Error {
  status: number
  code: string
  hint?: string
  operation?: Operation
  params?: Record<string, unknown>
  /** Seconds from the Retry-After header. */
  retryAfter?: number

  constructor(status: number, body: ApiErrorBody, retryAfter?: number) {
    super(body.error)
    this.status = status
    this.code = body.code
    this.hint = body.hint
    this.operation = body.operation
    this.params = body.params
    this.retryAfter = retryAfter
  }
}

let csrfToken = ''
export function setCsrfToken(token: string) {
  csrfToken = token
}

type Listener = () => void
const unauthorizedListeners = new Set<Listener>()
export function onUnauthorized(fn: Listener) {
  unauthorizedListeners.add(fn)
  return () => {
    unauthorizedListeners.delete(fn)
  }
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody = { error: t('error.http', { status: String(res.status) }), code: 'internal' }
  try {
    const parsed = (await res.json()) as ApiErrorBody
    if (parsed && typeof parsed.error === 'string') body = parsed
  } catch {
    // Non-JSON error bodies keep the generic message.
  }
  const retryAfter = Number(res.headers.get('Retry-After') ?? '')
  return new ApiError(res.status, body, Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : undefined)
}

export async function api<T>(method: string, path: string, body?: unknown, raw?: Blob): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'playkeeper' }
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrfToken
  let payload: BodyInit | undefined
  if (raw) {
    payload = raw
    headers['Content-Type'] = raw.type || 'application/gzip'
  } else if (body !== undefined) {
    payload = JSON.stringify(body)
    headers['Content-Type'] = 'application/json'
  }
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin', cache: 'no-store' })
  } catch {
    throw new ApiError(0, { error: t('error.network'), code: 'network' })
  }
  if (
    res.status === 401 &&
    !path.startsWith('/api/auth/login') &&
    !path.startsWith('/api/auth/second-factor') &&
    !path.startsWith('/api/setup')
  ) {
    unauthorizedListeners.forEach((fn) => fn())
  }
  if (!res.ok) throw await parseError(res)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const get = <T>(path: string) => api<T>('GET', path)
export const post = <T>(path: string, body: unknown = {}) => api<T>('POST', path, body)
export const del = <T>(path: string) => api<T>('DELETE', path)
export const put = <T>(path: string, body: unknown) => api<T>('PUT', path, body)
