// The live demo's API client: what src/api/client.ts exports, answered in the
// browser by the demo's make-believe panel instead of over the network.

import type * as real from '@/api/client'
import { ApiError, onUnauthorized, setCsrfToken } from '@/api/client'
import { answer } from './engine'
import { faceIndex } from './faces'

export { ApiError, onUnauthorized, setCsrfToken }

export const api = <T>(method: string, path: string, body?: unknown, raw?: Blob) => answer(method, path, body, raw) as Promise<T>
export const get = <T>(path: string) => api<T>('GET', path)
export const post = <T>(path: string, body: unknown = {}) => api<T>('POST', path, body)
export const del = <T>(path: string) => api<T>('DELETE', path)

/** A player's face: one of the demo's own, so the page never asks another site. */
export const playerHeadUrl = (name: string) => `${import.meta.env.BASE_URL}faces/${faceIndex(name)}.svg`

// Type-checks that this module stands in for every export of the real client.
void ({ ApiError, onUnauthorized, setCsrfToken, api, get, post, del, playerHeadUrl } satisfies typeof real)

// Links straight to the API, like a backup's Download, have no file behind
// them here: the demo answers them itself.
document.addEventListener(
  'click',
  (e) => {
    const link = e.target instanceof Element ? e.target.closest('a[href]') : null
    if (!(link instanceof HTMLAnchorElement)) return
    const url = new URL(link.href)
    if (url.origin !== window.location.origin || !url.pathname.startsWith('/api/')) return
    e.preventDefault()
    answer('GET', url.pathname + url.search).catch(() => undefined)
  },
  true,
)
