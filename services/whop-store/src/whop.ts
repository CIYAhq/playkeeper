/** The Whop API version the store reads, pinned as Whop asks of blueprints. */
export const apiVersion = '2026-09-29'

/**
 * What the store reads from its environment. Whop hosting sets
 * WHOP_API_ORIGIN and WHOP_ACCOUNT_ID, and adds the app's own key to the
 * requests the store makes to Whop's API, so the key never reaches this
 * code. WHOP_API_KEY is for running the store elsewhere, such as locally.
 */
export interface Env {
  WHOP_API_ORIGIN?: string
  WHOP_ACCOUNT_ID?: string
  WHOP_API_KEY?: string
}

export interface Account {
  id: string
  title: string
  description: string
  logoURL: string
  /** The terms the business uploaded to Whop, if any. */
  termsURL: string
}

export interface Product {
  id: string
  title: string
  headline: string
  visibility: string
  metadata: Record<string, string>
}

/** One way to buy a product. Whop's API calls plans variants now; their ids still start with plan_. */
export interface Plan {
  id: string
  productID: string
  title: string
  description: string
  /** visible, hidden (reachable only by its link), archived or quick_link. */
  visibility: string
  /** buy_now or waitlist. */
  releaseMethod: string
  /** renewal or one_time. */
  planType: string
  formattedPrice: string
  currency: string
  initialPrice: number
  renewalPrice: number
  /** Days between renewals. */
  billingPeriod: number
  trialDays: number
  stock: number
  unlimitedStock: boolean
  purchaseURL: string
  metadata: Record<string, string>
}

export interface StoreData {
  account: Account
  products: Product[]
  plans: Plan[]
}

export class WhopError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'WhopError'
  }
}

type Json = Record<string, unknown>

const obj = (v: unknown): Json => (v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Json) : {})
const str = (v: unknown) => (typeof v === 'string' ? v : '')
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)

/** Whop keeps metadata as strings; anything else reads as its JSON text. */
function metadata(v: unknown): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, x] of Object.entries(obj(v))) {
    if (typeof x === 'string') out[k] = x
    else if (x !== null && x !== undefined) out[k] = JSON.stringify(x)
  }
  return out
}

function account(v: unknown): Account {
  const o = obj(v)
  return { id: str(o.id), title: str(o.title), description: str(o.description), logoURL: str(o.logo_url), termsURL: str(obj(o.terms_of_service).url) }
}

function product(v: unknown): Product {
  const o = obj(v)
  return { id: str(o.id), title: str(o.title), headline: str(o.headline), visibility: str(o.visibility), metadata: metadata(o.metadata) }
}

function plan(v: unknown): Plan {
  const o = obj(v)
  return {
    id: str(o.id),
    productID: str(obj(o.product).id),
    title: str(o.title),
    description: str(o.description),
    visibility: str(o.visibility),
    releaseMethod: str(o.release_method),
    planType: str(o.plan_type),
    formattedPrice: str(o.formatted_price),
    currency: str(o.currency),
    initialPrice: num(o.initial_price),
    renewalPrice: num(o.renewal_price),
    billingPeriod: num(o.billing_period),
    trialDays: num(o.trial_period_days),
    stock: num(o.stock),
    unlimitedStock: o.unlimited_stock === true,
    purchaseURL: str(o.purchase_url),
    metadata: metadata(o.metadata),
  }
}

/** A setting from the Worker's bindings, or from process.env, where Whop's docs read them. */
function setting(env: Env, name: keyof Env) {
  const bound = env[name]
  if (typeof bound === 'string' && bound) return bound
  const proc = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process
  return proc?.env?.[name] ?? ''
}

/** At most this many pages of 100 are read of a list, far more than a store has. */
const maxPages = 10

/** Reads the business, its products and its plans from Whop's API. */
export async function readStore(env: Env, fetcher: typeof fetch = fetch): Promise<StoreData> {
  const origin = (setting(env, 'WHOP_API_ORIGIN') || 'https://api.whop.com').replace(/\/+$/, '')
  const key = setting(env, 'WHOP_API_KEY')
  const headers: Record<string, string> = { accept: 'application/json', 'api-version-date': apiVersion }
  if (key) headers.authorization = `Bearer ${key}`

  async function get(path: string, query: Record<string, string> = {}): Promise<Json> {
    const url = new URL(`${origin}/api/v1${path}`)
    for (const [k, v] of Object.entries(query)) url.searchParams.set(k, v)
    const res = await fetcher(url, { headers, signal: AbortSignal.timeout(8_000) })
    const text = await res.text()
    let body: unknown
    try {
      body = JSON.parse(text)
    } catch {
      body = undefined
    }
    if (!res.ok) {
      const message = str(obj(obj(body).error).message) || `Whop answered ${res.status}`
      throw new WhopError(res.status, `${path}: ${message}`)
    }
    if (body === undefined) throw new WhopError(res.status, `${path}: Whop's answer isn't JSON`)
    return obj(body)
  }

  async function list<T>(path: string, query: Record<string, string>, parse: (v: unknown) => T): Promise<T[]> {
    const out: T[] = []
    let after = ''
    for (let page = 0; page < maxPages; page++) {
      const body = await get(path, { ...query, first: '100', ...(after ? { after } : {}) })
      const data = Array.isArray(body.data) ? body.data : []
      out.push(...data.map(parse))
      const info = obj(body.page_info)
      after = str(info.end_cursor)
      if (info.has_next_page !== true || !after) break
    }
    return out
  }

  const me = account(await get('/accounts/me'))
  const accountID = setting(env, 'WHOP_ACCOUNT_ID') || me.id
  if (!accountID) throw new WhopError(0, "Whop didn't say which business the store belongs to")
  const [products, plans] = await Promise.all([
    list('/products', { account_id: accountID }, product),
    list('/variants', { account_id: accountID }, plan),
  ])
  return { account: me, products, plans }
}
