// The live demo's API client: what src/api/client.ts exports, answered in the
// browser by the demo's make-believe panel instead of over the network.

import type * as real from '@/api/client'
import { ApiError, onUnauthorized, responseError, setCsrfToken, writeHeaders } from '@/api/client'
import { addonIconOf, faceOf } from './data'
import { answer } from './engine'

export { ApiError, onUnauthorized, responseError, setCsrfToken, writeHeaders }

export const api = <T>(method: string, path: string, body?: unknown, raw?: Blob) => answer(method, path, body, raw) as Promise<T>
export const get = <T>(path: string) => api<T>('GET', path)
export const post = <T>(path: string, body: unknown = {}) => api<T>('POST', path, body)
export const del = <T>(path: string) => api<T>('DELETE', path)
export const put = <T>(path: string, body: unknown) => api<T>('PUT', path, body)

/** A player's face: one of the demo's own, so the page never asks another site. */
export const playerHeadUrl = (name: string) => `${import.meta.env.BASE_URL}faces/${faceOf(name)}.svg`

/** A sample add-on's icon: one of the demo's own, for the same reason. */
export const addonIconUrl = (_serverId: string, url: string) => {
  const n = addonIconOf(url)
  return n === undefined ? undefined : `${import.meta.env.BASE_URL}icons/${n}.svg`
}

/** A file saved through the API, like a recovery key: the demo answers the request itself, so nothing is saved. */
export const download = async (path: string, fallbackName: string): Promise<string> => {
  await answer('GET', path)
  return fallbackName
}

// Type-checks that this module stands in for every export of the real client.
void ({ ApiError, onUnauthorized, setCsrfToken, writeHeaders, responseError, api, get, post, put, del, playerHeadUrl, addonIconUrl, download } satisfies typeof real)

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
