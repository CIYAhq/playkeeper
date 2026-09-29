import { describe, expect, it } from 'vitest'
import { apiVersion, readStore, WhopError } from '../src/whop.ts'
import { fakeWhop, openStore } from './fake-whop.ts'

const env = { WHOP_API_ORIGIN: 'https://api.example.test', WHOP_ACCOUNT_ID: 'biz_pip' }

describe('reading the store from Whop', () => {
  it('reads the business, its products and plans with the pinned API version, leaving the key to Whop hosting', async () => {
    const whop = fakeWhop(openStore)
    const data = await readStore(env, whop.fetch)
    expect(data.account).toEqual({ id: 'biz_pip', title: 'Pip Hosting', description: '', logoURL: '', termsURL: '' })
    expect(data.products.map((p) => p.id)).toEqual(['prod_server', 'prod_course'])
    expect(data.plans.map((p) => p.id)).toEqual(['plan_plus', 'plan_starter', 'plan_test', 'plan_old', 'plan_link', 'plan_course'])
    expect(whop.seen.map((s) => `${s.url.origin}${s.url.pathname}?${s.url.searchParams}`)).toEqual([
      'https://api.example.test/api/v1/accounts/me?',
      'https://api.example.test/api/v1/products?account_id=biz_pip&first=100',
      'https://api.example.test/api/v1/variants?account_id=biz_pip&first=100',
    ])
    for (const s of whop.seen) {
      expect(s.headers.get('api-version-date')).toBe(apiVersion)
      expect(s.headers.get('authorization')).toBeNull()
    }
  })

  it('reads plans as Whop sends them, nulls and all', async () => {
    const data = await readStore(env, fakeWhop(openStore).fetch)
    const test = data.plans.find((p) => p.id === 'plan_test')
    expect(test).toEqual({
      id: 'plan_test',
      productID: 'prod_server',
      title: 'Owner test',
      description: '',
      visibility: 'hidden',
      releaseMethod: 'buy_now',
      planType: 'renewal',
      formattedPrice: '',
      currency: 'usd',
      initialPrice: 0,
      renewalPrice: 10,
      billingPeriod: 30,
      trialDays: 7,
      stock: 1,
      unlimitedStock: false,
      purchaseURL: 'https://whop.com/checkout/plan_test',
      metadata: { playkeeper_servers: '1', playkeeper_memory_gb: '4' },
    })
    expect(data.plans.find((p) => p.id === 'plan_course')?.metadata).toEqual({})
  })

  it('sends a key it was given, and asks Whop whose it is without an account', async () => {
    const whop = fakeWhop(openStore)
    await readStore({ WHOP_API_ORIGIN: 'https://api.example.test/', WHOP_API_KEY: 'apik_local' }, whop.fetch)
    expect(whop.seen.map((s) => s.headers.get('authorization'))).toEqual(['Bearer apik_local', 'Bearer apik_local', 'Bearer apik_local'])
    expect(whop.seen[1]?.url.searchParams.get('account_id')).toBe('biz_pip')
  })

  it('reads every page of a list', async () => {
    const whop = fakeWhop(openStore, { pageSize: 2 })
    const data = await readStore(env, whop.fetch)
    expect(data.plans).toHaveLength(6)
    expect(whop.seen.filter((s) => s.url.pathname.endsWith('/variants')).map((s) => s.url.searchParams.get('after'))).toEqual([null, '2', '4'])
  })

  it('says what Whop answered when it refuses', async () => {
    const whop = fakeWhop(openStore)
    const err = await readStore({ ...env, WHOP_ACCOUNT_ID: 'biz_other' }, whop.fetch).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(WhopError)
    expect((err as WhopError).status).toBe(403)
    expect((err as WhopError).message).toBe('/products: Not your account')
    const down = await readStore(env, fakeWhop(openStore, { down: () => true }).fetch).catch((e: unknown) => e)
    expect((down as WhopError).message).toBe('/accounts/me: Whop is down')
  })

  it('refuses an answer that is not JSON', async () => {
    const html = (async () => new Response('<html>Bad gateway</html>', { status: 200 })) as typeof fetch
    await expect(readStore(env, html)).rejects.toThrow("/accounts/me: Whop's answer isn't JSON")
  })
})
