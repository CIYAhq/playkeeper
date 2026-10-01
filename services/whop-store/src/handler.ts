import type { Html } from './html.ts'
import { homePage, notFoundPage, setupPage, termsPage, unavailablePage } from './pages.ts'
import { storefront, type Storefront } from './store.ts'
import { readStore, type Env } from './whop.ts'

/** How long a read of the store is shown before Whop is asked again. */
export const fresh = 60_000
/** How long an older read is still shown while Whop can't be reached. */
export const stale = 60 * 60_000

export interface Deps {
  fetch: typeof fetch
  now: () => number
}

function page(body: Html, status: number, cache: string) {
  return new Response(`<!doctype html>\n${body.value}`, {
    status,
    headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': cache, 'x-content-type-options': 'nosniff' },
  })
}

/**
 * The store's request handler. Each Worker instance keeps its last read of
 * the store for a minute, so visitors don't each wait on Whop's API, and
 * keeps showing it for an hour while Whop can't be reached. A read that
 * failed isn't tried again for a minute either, so an outage doesn't keep
 * every visitor waiting on Whop. Each request reads for itself, since a
 * Worker can't count on one request's fetch to answer another's.
 */
export function createHandler(deps: Deps = { fetch: (input, init) => fetch(input, init), now: () => Date.now() }) {
  let last: { at: number; store: Storefront } | undefined
  let failedAt = -Infinity

  async function store(env: Env): Promise<Storefront | undefined> {
    const now = deps.now()
    const shown = () => (last && now - last.at < stale ? last.store : undefined)
    if (last && now - last.at < fresh) return last.store
    if (now - failedAt < fresh) return shown()
    try {
      const s = storefront(await readStore(env, deps.fetch))
      last = { at: now, store: s }
      return s
    } catch (err) {
      failedAt = deps.now()
      console.error(`Reading the store from Whop failed: ${err instanceof Error ? err.message : String(err)}`)
      return shown()
    }
  }

  return async function handle(request: Request, env: Env): Promise<Response> {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Only GET and HEAD work here.\n', { status: 405, headers: { allow: 'GET, HEAD', 'content-type': 'text/plain; charset=utf-8' } })
    }
    const path = new URL(request.url).pathname.replace(/\/+$/, '') || '/'
    const s = await store(env)
    if (path !== '/' && path !== '/terms' && path !== '/setup') return page(notFoundPage(s), 404, 'public, max-age=60')
    if (!s) return page(unavailablePage(), 503, 'no-store')
    if (path === '/setup') {
      // A store taking orders is set up already, so its owner lands on its plans.
      if (s.shelves.length > 0) return new Response(null, { status: 302, headers: { location: '/', 'cache-control': 'public, max-age=60' } })
      return page(setupPage(s), 200, 'public, max-age=60')
    }
    return page(path === '/' ? homePage(s) : termsPage(s), 200, 'public, max-age=60')
  }
}
