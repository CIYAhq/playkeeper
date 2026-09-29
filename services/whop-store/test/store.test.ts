import { describe, expect, it } from 'vitest'
import { checkoutURL, planAllowance, price, storefront } from '../src/store.ts'
import { readStore, type Plan, type StoreData } from '../src/whop.ts'
import { dashboard, fakeWhop, freshCopy, openStore, plan, type Catalogue } from './fake-whop.ts'

const read = (c: () => Catalogue) => readStore({ WHOP_API_ORIGIN: 'https://api.example.test' }, fakeWhop(c).fetch)

async function data(change: (c: Catalogue) => void = () => {}): Promise<StoreData> {
  const c = openStore()
  change(c)
  return read(() => c)
}

describe('a plan’s allowance', () => {
  it('is read as Playkeeper reads it: a count of servers and whole or half GB', () => {
    expect(planAllowance({ playkeeper_servers: '1', playkeeper_memory_gb: '4' })).toEqual({ servers: 1, memoryGB: 4 })
    expect(planAllowance({ playkeeper_servers: ' 2 ', playkeeper_memory_gb: '8.5' })).toEqual({ servers: 2, memoryGB: 8.5 })
    for (const [servers, gb] of [['0', '4'], ['1', '0'], ['1', '4.25'], ['one', '4'], ['1', ''], ['', '4'], ['1', '2048'], ['-1', '4'], ['1.5', '4']]) {
      expect(planAllowance({ playkeeper_servers: servers ?? '', playkeeper_memory_gb: gb ?? '' }), `${servers} servers, ${gb} GB`).toBeNull()
    }
    expect(planAllowance({})).toBeNull()
  })
})

describe('a plan’s checkout link', () => {
  it('is kept only when it’s an https link on Whop', () => {
    expect(checkoutURL('https://whop.com/checkout/plan_a')).toBe('https://whop.com/checkout/plan_a')
    expect(checkoutURL('https://sub.whop.com/checkout/plan_a')).toBe('https://sub.whop.com/checkout/plan_a')
    for (const bad of ['http://whop.com/checkout/plan_a', 'https://whop.com.evil.test/', 'https://evilwhop.com/', 'javascript:alert(1)', 'https://user:pw@whop.com/', 'not a url', '']) {
      expect(checkoutURL(bad), bad).toBe('')
    }
  })
})

describe('a plan’s price', () => {
  const p = (fields: Partial<Plan>): Plan => ({
    id: 'plan_x', productID: 'prod_x', title: '', description: '', visibility: 'visible', releaseMethod: 'buy_now', planType: 'renewal',
    formattedPrice: '', currency: 'usd', initialPrice: 0, renewalPrice: 8, billingPeriod: 30, trialDays: 0, stock: 0, unlimitedStock: true, purchaseURL: '', metadata: {},
    ...fields,
  })
  it('is Whop’s own words when it sends them', () => {
    expect(price(p({ formattedPrice: '$8.00 / month' }))).toBe('$8.00 / month')
  })
  it('is otherwise the amount and how often it renews', () => {
    expect(price(p({}))).toBe('$8.00 / month')
    expect(price(p({ currency: 'eur', renewalPrice: 16, billingPeriod: 365 }))).toBe('€16.00 / year')
    expect(price(p({ billingPeriod: 45 }))).toBe('$8.00 / 45 days')
    expect(price(p({ planType: 'one_time', initialPrice: 20 }))).toBe('$20.00')
    expect(price(p({ currency: 'xyz?' }))).toBe('XYZ? 8.00 / month')
  })
})

describe('what the store shows', () => {
  it('offers the visible plans of the products a connected Playkeeper sells, cheapest first', async () => {
    const s = storefront(await data())
    expect(s.name).toBe('Pip Hosting')
    expect(s.dashboard).toBe(`${dashboard}/`)
    expect(s.shelves).toHaveLength(1)
    expect(s.shelves[0]?.title).toBe('Minecraft server')
    expect(s.shelves[0]?.headline).toBe('A Java Edition server of your own')
    expect(s.shelves[0]?.offers).toEqual([
      { id: 'plan_starter', name: 'Starter', description: '', price: '$8.00 / month', trialDays: 3, allowance: { servers: 1, memoryGB: 4 }, action: { kind: 'buy', url: 'https://whop.com/checkout/plan_starter' } },
      { id: 'plan_plus', name: 'Plus', description: '', price: '$16.00 / month', trialDays: 0, allowance: { servers: 2, memoryGB: 8 }, action: { kind: 'buy', url: 'https://whop.com/checkout/plan_plus' } },
    ])
  })

  it('takes no orders in a fresh copy of the blueprint, before its Playkeeper is connected', async () => {
    const s = storefront(await read(freshCopy))
    expect(s.name).toBe('Joe’s Hosting')
    expect(s.shelves).toEqual([])
    expect(s.dashboard).toBe('')
  })

  it('takes no orders while every plan is hidden, as the demo store keeps them, but still lets customers sign in', async () => {
    const s = storefront(await data((c) => (c.plans = c.plans.map((p) => ({ ...p, visibility: 'hidden' })))))
    expect(s.shelves).toEqual([])
    expect(s.dashboard).toBe(`${dashboard}/`)
  })

  it('never offers a product that is archived, or marked with anything but an https address', async () => {
    const archived = storefront(await data((c) => (c.products[0] = { ...c.products[0], visibility: 'archived' })))
    expect(archived.shelves).toEqual([])
    for (const marker of ['http://beta.playkeeper.me', 'javascript:alert(1)', 'beta.playkeeper.me', '']) {
      const s = storefront(await data((c) => (c.products[0] = { ...c.products[0], metadata: { playkeeper_dashboard: marker } })))
      expect(s.shelves, marker).toEqual([])
      expect(s.dashboard, marker).toBe('')
    }
  })

  it('leaves out a plan whose checkout isn’t on Whop', async () => {
    const s = storefront(await data((c) => (c.plans[1] = { ...c.plans[1], purchase_url: 'https://evil.test/checkout' })))
    expect(s.shelves[0]?.offers.map((o) => o.id)).toEqual(['plan_plus'])
  })

  it('shows a sold-out plan as sold out, and a waitlist as a waitlist', async () => {
    const s = storefront(await data((c) => {
      c.plans[0] = { ...c.plans[0], unlimited_stock: false, stock: 0 }
      c.plans[1] = { ...c.plans[1], release_method: 'waitlist' }
    }))
    expect(s.shelves[0]?.offers.map((o) => [o.id, o.action.kind])).toEqual([['plan_starter', 'waitlist'], ['plan_plus', 'sold-out']])
  })

  it('shows a plan without an allowance, named after its product when it has no name', async () => {
    const s = storefront(await data((c) => c.plans.push(plan('plan_plain', { renewal_price: 4, metadata: {} }))))
    expect(s.shelves[0]?.offers[0]).toMatchObject({ id: 'plan_plain', name: 'Minecraft server', allowance: null })
  })

  it('shows each product’s plans on a shelf of its own, the cheapest shelf first', async () => {
    const s = storefront(await data((c) => {
      c.products.push({ id: 'prod_modded', title: 'Modded server', headline: null, visibility: 'visible', metadata: { playkeeper_dashboard: dashboard } })
      c.plans.push(plan('plan_modded', { title: 'Modded', renewal_price: 4, product: { id: 'prod_modded', title: 'Modded server' } }))
    }))
    expect(s.shelves.map((sh) => [sh.title, sh.offers.map((o) => o.id)])).toEqual([['Modded server', ['plan_modded']], ['Minecraft server', ['plan_starter', 'plan_plus']]])
  })

  it('uses the business’s logo and terms only from https links', async () => {
    const good = storefront(await data((c) => (c.account = { ...c.account, logo_url: 'https://assets.whop.com/logo.png', terms_of_service: { url: 'https://assets.whop.com/terms.pdf' } })))
    expect([good.logo, good.terms]).toEqual(['https://assets.whop.com/logo.png', 'https://assets.whop.com/terms.pdf'])
    const bad = storefront(await data((c) => (c.account = { ...c.account, logo_url: 'javascript:alert(1)', terms_of_service: { url: 'http://example.test/terms' } })))
    expect([bad.logo, bad.terms]).toEqual(['', ''])
  })
})
