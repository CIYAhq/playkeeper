import type { Plan, Product, StoreData } from './whop.ts'

// Metadata Playkeeper's Sell on Whop reads and writes (internal/whop in the
// Playkeeper repository). A plan's servers and memory say what a buyer of it
// may create; a product's dashboard is the address of the Playkeeper that
// sells it, which it sets once it's connected, with the business it marked
// the product in.
export const metaServers = 'playkeeper_servers'
export const metaMemoryGB = 'playkeeper_memory_gb'
export const metaDashboard = 'playkeeper_dashboard'
export const metaBusiness = 'playkeeper_business'

export interface Allowance {
  servers: number
  memoryGB: number
}

/** What a plan's button does: go to Whop's checkout, join the plan's waitlist there, or nothing, when it's sold out. */
export type Action = { kind: 'buy'; url: string } | { kind: 'waitlist'; url: string } | { kind: 'sold-out' }

export interface Offer {
  id: string
  name: string
  description: string
  price: string
  trialDays: number
  allowance: Allowance | null
  action: Action
}

/** A product the store sells, with its plans on offer, cheapest first. */
export interface Shelf {
  id: string
  title: string
  headline: string
  offers: Offer[]
}

export interface Storefront {
  name: string
  description: string
  logo: string
  /** Where customers sign in: the Playkeeper that sells the store's products. */
  dashboard: string
  /** The business's own terms on Whop, or "" for the store's. */
  terms: string
  /** Empty while the store isn't taking orders. */
  shelves: Shelf[]
}

/**
 * What a plan's metadata lets a buyer create, read as Playkeeper reads it:
 * a count of servers, and memory in whole or half GB. null when the plan
 * says nothing, or something else.
 */
export function planAllowance(meta: Record<string, string>): Allowance | null {
  const servers = (meta[metaServers] ?? '').trim()
  const gb = (meta[metaMemoryGB] ?? '').trim()
  if (!/^\+?\d+$/.test(servers) || !/^\d+(\.\d+)?$/.test(gb)) return null
  const n = Number(servers)
  const g = Number(gb)
  if (n < 1 || !Number.isSafeInteger(n) || g <= 0 || g > 1024 || !Number.isInteger(g * 2)) return null
  return { servers: n, memoryGB: g }
}

function httpsURL(raw: string): URL | null {
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' && !u.username && !u.password ? u : null
  } catch {
    return null
  }
}

/** A plan's checkout link, if it's on Whop. */
export function checkoutURL(raw: string) {
  const u = httpsURL(raw)
  return u && (u.hostname === 'whop.com' || u.hostname.endsWith('.whop.com')) ? u.href : ''
}

const usual: Record<number, string> = { 1: 'day', 7: 'week', 14: '2 weeks', 30: 'month', 31: 'month', 90: '3 months', 180: '6 months', 365: 'year' }

/** A plan's price in Whop's words, or else its amount and how often it renews. */
export function price(p: Plan) {
  if (p.formattedPrice) return p.formattedPrice
  let amount: string
  try {
    amount = new Intl.NumberFormat('en', { style: 'currency', currency: p.currency.toUpperCase() || 'USD' }).format(p.planType === 'renewal' ? p.renewalPrice : p.initialPrice)
  } catch {
    amount = `${p.currency.toUpperCase()} ${(p.planType === 'renewal' ? p.renewalPrice : p.initialPrice).toFixed(2)}`
  }
  if (p.planType !== 'renewal' || p.billingPeriod <= 0) return amount
  const every = usual[p.billingPeriod] ?? `${p.billingPeriod} days`
  return `${amount} / ${every}`
}

const cost = (p: Plan) => (p.planType === 'renewal' ? p.renewalPrice : p.initialPrice)

function action(p: Plan, url: string): Action {
  if (!p.unlimitedStock && p.stock <= 0) return { kind: 'sold-out' }
  return p.releaseMethod === 'waitlist' ? { kind: 'waitlist', url } : { kind: 'buy', url }
}

/**
 * What the store shows: the visible plans of the products a connected
 * Playkeeper sells. A product counts once Playkeeper has put its address on
 * it for this business, so a copy of the store, even one that kept the
 * marking of the store it was copied from, takes no orders before there's
 * a machine to run the servers. A plan counts only while it's visible on
 * Whop and has a checkout link there. Hidden plans stay reachable by their
 * own links but never appear here.
 */
export function storefront(data: StoreData): Storefront {
  const selling = new Map<string, { product: Product; dashboard: string }>()
  for (const p of data.products) {
    const dashboard = httpsURL(p.metadata[metaDashboard] ?? '')
    if (!dashboard || p.metadata[metaBusiness] !== data.business || p.visibility === 'archived') continue
    selling.set(p.id, { product: p, dashboard: dashboard.href })
  }
  const offered = new Map<string, { shelf: Shelf; plans: Plan[] }>()
  for (const plan of data.plans) {
    const seller = selling.get(plan.productID)
    const url = checkoutURL(plan.purchaseURL)
    if (!seller || plan.visibility !== 'visible' || !url) continue
    let entry = offered.get(plan.productID)
    if (!entry) {
      entry = { shelf: { id: seller.product.id, title: seller.product.title, headline: seller.product.headline, offers: [] }, plans: [] }
      offered.set(plan.productID, entry)
    }
    entry.plans.push(plan)
  }
  const shelves = [...offered.values()].map(({ shelf, plans }) => {
    const sorted = [...plans].sort((a, b) => cost(a) - cost(b) || (a.title || shelf.title).localeCompare(b.title || shelf.title))
    shelf.offers = sorted.map((p) => ({
      id: p.id,
      name: p.title || shelf.title,
      description: p.description,
      price: price(p),
      trialDays: Math.max(0, Math.floor(p.trialDays)),
      allowance: planAllowance(p.metadata),
      action: action(p, checkoutURL(p.purchaseURL)),
    }))
    return { shelf, from: Math.min(...plans.map(cost)) }
  })
  shelves.sort((a, b) => a.from - b.from || a.shelf.title.localeCompare(b.shelf.title))
  const logo = httpsURL(data.account.logoURL)
  const terms = httpsURL(data.account.termsURL)
  return {
    name: data.account.title.trim(),
    description: data.account.description.trim(),
    logo: logo ? logo.href : '',
    dashboard: [...selling.values()][0]?.dashboard ?? '',
    terms: terms ? terms.href : '',
    shelves: shelves.map((s) => s.shelf),
  }
}
