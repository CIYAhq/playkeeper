// A make-believe Whop API for the store's tests: a business, its products and
// plans in the shapes Whop's API answers with, and a fetch that serves them.

export const dashboard = 'https://beta.playkeeper.me'

export type Json = Record<string, unknown>

export interface Catalogue {
  account: Json
  products: Json[]
  plans: Json[]
}

export function plan(id: string, fields: Json = {}): Json {
  return {
    id,
    title: null,
    description: null,
    visibility: 'visible',
    release_method: 'buy_now',
    plan_type: 'renewal',
    formatted_price: null,
    currency: 'usd',
    initial_price: 0,
    renewal_price: 8,
    billing_period: 30,
    trial_period_days: null,
    stock: 0,
    unlimited_stock: true,
    purchase_url: `https://whop.com/checkout/${id}`,
    product: { id: 'prod_server', title: 'Minecraft server' },
    metadata: { playkeeper_servers: '1', playkeeper_memory_gb: '4' },
    ...fields,
  }
}

/** Pip Hosting as the demo store has it once Playkeeper is connected: two plans on sale, and the hidden ones that never show. */
export function openStore(): Catalogue {
  return {
    account: { id: 'biz_pip', title: 'Pip Hosting', description: null, logo_url: null, terms_of_service: null },
    products: [
      { id: 'prod_server', title: 'Minecraft server', headline: 'A Java Edition server of your own', visibility: 'visible', metadata: { playkeeper_dashboard: dashboard } },
      { id: 'prod_course', title: 'Server growth course', headline: null, visibility: 'visible', metadata: {} },
    ],
    plans: [
      plan('plan_plus', { title: 'Plus', renewal_price: 16, formatted_price: '$16.00 / month', metadata: { playkeeper_servers: '2', playkeeper_memory_gb: '8' } }),
      plan('plan_starter', { title: 'Starter', formatted_price: '$8.00 / month', trial_period_days: 3 }),
      plan('plan_test', { title: 'Owner test', visibility: 'hidden', trial_period_days: 7, unlimited_stock: false, stock: 1 }),
      plan('plan_old', { title: 'Old', visibility: 'archived' }),
      plan('plan_link', { title: 'Quick link', visibility: 'quick_link' }),
      plan('plan_course', { title: 'Course', renewal_price: 30, product: { id: 'prod_course', title: 'Server growth course' }, metadata: null }),
    ],
  }
}

/** A fresh copy of the blueprint: the same plans, before any Playkeeper is connected. */
export function freshCopy(): Catalogue {
  const c = openStore()
  c.account = { ...c.account, id: 'biz_copy', title: 'Joe’s Hosting' }
  c.products = c.products.map((p) => ({ ...p, metadata: {} }))
  return c
}

export interface Seen {
  url: URL
  headers: Headers
}

/**
 * A fetch that answers as Whop's API would from the catalogue, which it
 * reads on each request so a test can change it. pageSize splits lists into
 * pages; down makes every request fail as a Whop outage would.
 */
export function fakeWhop(catalogue: () => Catalogue, opts: { pageSize?: number; down?: () => boolean } = {}) {
  const seen: Seen[] = []
  const fetcher = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input))
    seen.push({ url, headers: new Headers(init?.headers) })
    const answer = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
    if (opts.down?.()) return answer(503, { error: { type: 'unavailable', message: 'Whop is down' } })
    const c = catalogue()
    const page = (items: Json[]) => {
      const size = opts.pageSize ?? 100
      const start = Number(url.searchParams.get('after') ?? 0)
      const data = items.slice(start, start + size)
      const more = start + size < items.length
      return answer(200, { data, page_info: { has_next_page: more, end_cursor: more ? String(start + size) : null } })
    }
    const account = url.searchParams.get('account_id')
    switch (url.pathname) {
      case '/api/v1/accounts/me':
        return answer(200, c.account)
      case '/api/v1/products':
        return account === c.account.id ? page(c.products) : answer(403, { error: { type: 'forbidden', message: 'Not your account' } })
      case '/api/v1/variants':
        return account === c.account.id ? page(c.plans) : answer(403, { error: { type: 'forbidden', message: 'Not your account' } })
      default:
        return answer(404, { error: { type: 'not_found', message: `Unrecognized request URL (GET: ${url.pathname})` } })
    }
  }) as typeof fetch
  return { fetch: fetcher, seen }
}
