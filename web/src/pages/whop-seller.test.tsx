// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { SellerPrices, SellerView } from '@/api/types'
import { href, parse } from '@/lib/router'
import { WhopSellerPage } from './whop-seller'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => new Promise(() => {})),
}))

let root: Root | undefined

async function render(store: string): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WhopSellerPage store={store} />))
  await act(async () => {})
  return document.body.textContent ?? ''
}

const starter = { id: 'plan_starter', title: 'Starter', servers: 1, memoryMB: 4096, price: 1000, currency: 'usd', floor: 1200, share: 850, suggested: 1500, settable: true }
const plus = { id: 'plan_plus', title: 'Plus', servers: 2, memoryMB: 8192, price: 2000, currency: 'usd', floor: 2400, share: 1700, suggested: 3000, settable: true }
const closed: SellerPrices = { plans: [starter, plus], canOpen: true, canUpdate: false, fixable: false }
const open: SellerPrices = { ...closed, plans: [{ ...starter, price: 1500 }, { ...plus, price: 3000 }], canOpen: false, canUpdate: true }
const view = (state: SellerView['store']['state'], over: Partial<SellerView> = {}): SellerView => ({
  store: { id: 'biz_other', title: 'Other Hosting', state, why: state === 'closed' ? 'Not open yet' : undefined },
  plans: [],
  customers: [],
  earnings: [],
  ...over,
})

/** Answers the page's reads with prices and view, as the store stands now. */
function reads(prices: () => SellerPrices, seller: () => SellerView) {
  vi.mocked(client.get).mockImplementation((path: string) => Promise.resolve(path.endsWith('/prices') ? prices() : seller()))
}

const heading = () => document.querySelector('h1')?.textContent
const button = (text: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === text)
const field = (label: string) => {
  const l = [...document.querySelectorAll('label')].find((x) => x.textContent?.trim() === label)
  return (l && (document.getElementById(l.htmlFor) as HTMLInputElement | null)) ?? undefined
}

async function click(el: HTMLElement | undefined) {
  if (!el) throw new Error('nothing to click')
  await act(async () => el.click())
  await act(async () => {})
}

beforeEach(() => {
  vi.mocked(client.post).mockReset()
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
})

describe('a seller’s page inside Whop', () => {
  it('is at /whop/seller/<business>, and nothing else is', () => {
    expect(parse('/whop/seller/biz_f8DXrJvgQa0N6i')).toEqual({ name: 'whop-seller', store: 'biz_f8DXrJvgQa0N6i' })
    expect(href({ name: 'whop-seller', store: 'biz_f8DXrJvgQa0N6i' })).toBe('/whop/seller/biz_f8DXrJvgQa0N6i')
    expect(parse('/whop/seller/user_x')).toEqual({ name: 'whop-seller', store: '' })
    expect(parse('/whop/seller/biz_x/more')).toEqual({ name: 'whop-seller', store: '' })
    expect(parse('/whop')).toEqual({ name: 'home' })
  })

  it('walks a new seller through their prices, opening the store and going live, one step on screen at a time', async () => {
    let prices = closed
    let seller = view('closed')
    reads(
      () => prices,
      () => seller,
    )
    vi.mocked(client.post).mockImplementation((path: string) => {
      if (path.endsWith('/open')) return Promise.resolve({ store: { id: 'biz_other', title: 'Other Hosting', route: 'other-hosting' }, new: true })
      if (path.endsWith('/prices')) return Promise.resolve(prices)
      prices = open
      seller = view('selling')
      return Promise.resolve({ open: true })
    })
    await render('biz_other')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/open')
    expect(heading()).toBe('Check your prices')
    expect(document.activeElement).toBe(document.querySelector('h1'))
    expect(document.querySelector('[aria-current="step"]')?.textContent).toContain('Prices')
    expect(field('Starter')?.value).toBe('15.00')
    expect(field('Plus')?.value).toBe('30.00')
    expect(document.body.textContent).not.toContain('Open your store')

    await click(button('Looks good'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_starter', price: '15.00' })
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_plus', price: '30.00' })
    expect(heading()).toBe('Open your store')
    expect(document.querySelector('[aria-current="step"]')?.textContent).toContain('Open')
    expect(document.body.textContent).not.toContain('Check your prices')

    await click(button('Back'))
    expect(heading()).toBe('Check your prices')
    await click(button('Looks good'))
    await click(button('Open my store'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/sell')
    expect(heading()).toBe('You’re live')
    const visit = [...document.querySelectorAll('a')].find((a) => a.textContent?.includes('Visit your store'))
    expect(visit?.getAttribute('href')).toBe('https://whop.com/other-hosting')
    expect(visit?.getAttribute('target')).toBe('_blank')
    expect(document.body.textContent).toContain('No sales yet.')
    expect(document.body.textContent).toContain('No customers yet.')
  })

  it('shows an open store live straight away, with no steps, and says what it needs', async () => {
    reads(
      () => open,
      () => view('selling'),
    )
    vi.mocked(client.post).mockResolvedValueOnce({ store: { id: 'biz_other', title: 'Other Hosting', route: 'other-hosting', problem: 'The grant lacks plan:basic:read.' }, new: false })
    const text = await render('biz_other')
    expect(heading()).toBe('You’re live')
    expect(document.querySelector('[aria-current="step"]')).toBeNull()
    expect(text).toContain('Needs a look: The grant lacks plan:basic:read.')
  })

  it('says a store Playkeeper paused, or one that left, isn’t selling, with nothing to press', async () => {
    reads(
      () => ({ ...closed, canOpen: false }),
      () => view('suspended'),
    )
    vi.mocked(client.post).mockResolvedValueOnce({ store: { id: 'biz_other', title: 'Other Hosting' }, new: false })
    expect(await render('biz_other')).toContain('Your store is pausedPlaykeeper paused it. Ask Playkeeper why.')
    expect(document.querySelectorAll('button')).toHaveLength(0)
    reads(
      () => ({ ...closed, canOpen: false }),
      () => view('left'),
    )
    vi.mocked(client.post).mockResolvedValueOnce({ store: { id: 'biz_other', title: 'Other Hosting' }, new: false })
    expect(await render('biz_other')).toContain('Your store is off')
  })

  it('sends a business that hasn’t approved the app to approve it in Whop', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(409, {
        code: 'whop_not_approved',
        error: 'This business hasn’t approved everything Playkeeper Cloud asks for.',
        params: { installUrl: 'https://whop.com/apps/app_6oyNYgGluUMTx4/install', lacking: ['plan:basic:read'] },
      }),
    )
    await render('biz_other')
    expect(heading()).toBe('Approve Playkeeper Cloud')
    const link = [...document.querySelectorAll('a')].find((a) => a.textContent?.includes('Approve in Whop'))
    expect(link?.getAttribute('href')).toBe('https://whop.com/apps/app_6oyNYgGluUMTx4/install')
    expect(link?.getAttribute('target')).toBe('_blank')
    expect(vi.mocked(client.get)).not.toHaveBeenCalled()
  })

  it('refuses someone who isn’t on the business’s team, and a page opened outside Whop', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(403, { code: 'whop_not_team', error: 'Only the business’s team…' }))
    expect(await render('biz_other')).toContain('Only this business’s team on Whop can open its Playkeeper Cloud page.')
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(401, { code: 'whop_token', error: 'This page opens inside…' }))
    expect(await render('biz_other')).toContain('Open this page from your Whop dashboard, which tells Playkeeper who you are.')
    expect(vi.mocked(client.get)).not.toHaveBeenCalled()
  })

  it('calls nothing for an address that can’t be a business’s page', async () => {
    expect(await render('')).toContain('This address isn’t a business’s Playkeeper Cloud page.')
    expect(vi.mocked(client.post)).not.toHaveBeenCalled()
  })
})
