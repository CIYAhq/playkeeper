import type { ApiErrorBody, Operation } from './types'

export class ApiError extends Error {
  status: number
  code: string
  hint?: string
  operation?: Operation

  constructor(status: number, body: ApiErrorBody) {
    super(body.error)
    this.status = status
    this.code = body.code
    this.hint = body.hint
    this.operation = body.operation
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
  let body: ApiErrorBody = { error: `Request failed (HTTP ${res.status})`, code: 'internal' }
  try {
    const parsed = (await res.json()) as ApiErrorBody
    if (parsed && typeof parsed.error === 'string') body = parsed
  } catch {
    // Non-JSON error bodies keep the generic message.
  }
  return new ApiError(res.status, body)
}

export async function api<T>(method: string, path: string, body?: unknown, raw?: Blob): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'playkeeper' }
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrfToken
  let payload: BodyInit | undefined
  if (raw) {
    payload = raw
    headers['Content-Type'] = 'application/gzip'
  } else if (body !== undefined) {
    payload = JSON.stringify(body)
    headers['Content-Type'] = 'application/json'
  }
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin', cache: 'no-store' })
  } catch {
    throw new ApiError(0, { error: 'Cannot reach the Playkeeper panel. Check your connection or whether the server is running.', code: 'network' })
  }
  if (res.status === 401 && !path.startsWith('/api/auth/login') && !path.startsWith('/api/setup')) {
    unauthorizedListeners.forEach((fn) => fn())
  }
  if (!res.ok) throw await parseError(res)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const get = <T>(path: string) => api<T>('GET', path)
export const post = <T>(path: string, body: unknown = {}) => api<T>('POST', path, body)
export const del = <T>(path: string) => api<T>('DELETE', path)
