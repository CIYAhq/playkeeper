// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { SellerPrices } from '@/api/types'
import { PricesStep } from './whop-seller-prices'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => new Promise(() => {})),
}))

let root: Root | undefined
const onPrices = vi.fn()
const onSaved = vi.fn()
const onDone = vi.fn()
const onBack = vi.fn()

async function render(prices: SellerPrices, more: { notice?: string; edit?: boolean } = {}): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () =>
    r.render(<PricesStep store="biz_other" prices={prices} onPrices={onPrices} onSaved={onSaved} onDone={onDone} onBack={more.edit ? onBack : undefined} {...more} />),
  )
  return document.body.textContent ?? ''
}

const starter = { id: 'plan_starter', title: 'Starter', servers: 1, memoryMB: 4096, price: 1000, currency: 'usd', floor: 1200, share: 850, suggested: 1500, settable: true }
const plus = { id: 'plan_plus', title: 'Plus', servers: 2, memoryMB: 8192, price: 2600, currency: 'usd', floor: 2400, share: 1700, suggested: 3000, settable: true }
const pricesWith = (over: Partial<SellerPrices> = {}): SellerPrices => ({ plans: [starter, plus], canOpen: true, canUpdate: false, fixable: false, ...over })

function field(label: string): HTMLInputElement {
  const l = [...document.querySelectorAll('label')].find((x) => x.textContent?.trim() === label)
  const input = l && (document.getElementById(l.htmlFor) as HTMLInputElement | null)
  if (!input) throw new Error(`no field labelled ${label}`)
  return input
}

function button(text: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === text)
  if (!b) throw new Error(`no button ${text}`)
  return b
}

/** Types into an input the way a browser does, so React sees the change. */
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

beforeEach(() => {
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
  onPrices.mockReset()
  onSaved.mockReset()
  onDone.mockReset()
  onBack.mockReset()
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
})

describe('checking a seller’s prices', () => {
  it('offers each plan at its price, or at the price it suggests when that’s under the floor, with what it gives', async () => {
    const text = await render(pricesWith())
    expect(document.querySelector('h1')?.textContent).toBe('Check your prices')
    expect(text).toContain('This is what customers pay each month.')
    expect(field('Starter').value).toBe('15.00')
    expect(field('Plus').value).toBe('26.00')
    expect(text).toContain('1 server · 4 GB')
    expect(text).toContain('2 servers · 8 GB')
    expect(text).not.toContain('floor')
  })

  it('saves only the prices that changed, through relative addresses, and then goes on', async () => {
    await render(pricesWith())
    const saved = pricesWith({ plans: [{ ...starter, price: 1500 }, plus] })
    vi.mocked(client.post).mockResolvedValueOnce(saved)
    await click(button('Looks good'))
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_starter', price: '15.00' })
    expect(onDone).toHaveBeenCalledWith(saved)
  })

  it('says in a line under its plan what’s wrong with a price, and saves nothing', async () => {
    await render(pricesWith())
    await type(field('Starter'), '11')
    await type(field('Plus'), 'thirty')
    await click(button('Looks good'))
    const alerts = [...document.querySelectorAll('[role="alert"]')].map((a) => a.textContent)
    expect(alerts).toEqual(['At least $12.00', 'Write a price, like 15'])
    expect(field('Starter').getAttribute('aria-invalid')).toBe('true')
    expect(vi.mocked(client.post)).not.toHaveBeenCalled()
    expect(onDone).not.toHaveBeenCalled()
  })

  it('says why Whop refused a price under its plan, and stays', async () => {
    await render(pricesWith())
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(502, { code: 'retry_later', error: 'Whop didn’t answer. Try again in a moment.' }))
    await click(button('Looks good'))
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('Whop didn’t answer. Try again in a moment.')
    expect(onDone).not.toHaveBeenCalled()
  })

  it('keeps the prices it saved when a later one fails, without starting the step again', async () => {
    await render(pricesWith())
    await type(field('Plus'), '28')
    const saved = pricesWith({ plans: [{ ...starter, price: 1500 }, plus] })
    vi.mocked(client.post)
      .mockResolvedValueOnce(saved)
      .mockRejectedValueOnce(new client.ApiError(502, { code: 'retry_later', error: 'Whop didn’t answer. Try again in a moment.' }))
    await click(button('Looks good'))
    expect(onSaved).toHaveBeenCalledWith(saved)
    expect(onPrices).not.toHaveBeenCalled()
    expect(onDone).not.toHaveBeenCalled()
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('Whop didn’t answer. Try again in a moment.')
  })

  it('offers Fix my plans alone while a plan needs one, and tells the page what it changed', async () => {
    const text = await render(pricesWith({ fixable: true }))
    expect(text).toContain('Some plans need a quick fix first.')
    expect(document.querySelectorAll('input')).toHaveLength(0)
    expect(document.querySelectorAll('button')).toHaveLength(1)
    const fixed = pricesWith()
    vi.mocked(client.post).mockResolvedValueOnce({ changed: 'Starter now renews every month.', prices: fixed })
    await click(button('Fix my plans'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/fix')
    expect(onPrices).toHaveBeenCalledWith(fixed, 'Starter now renews every month.')
  })

  it('says in a line what Fix my plans changed', async () => {
    await render(pricesWith(), { notice: 'Starter now renews every month.' })
    expect(document.querySelector('[role="status"]')?.textContent).toBe('Done. Starter now renews every month.')
  })

  it('says plainly what to change in Whop when Fix my plans can’t, and checks again', async () => {
    const line = 'Starter and Plus are on the same product. Give each its own product in Whop.'
    const text = await render(pricesWith({ blocked: [line] }))
    expect(text).toContain('Change this in Whop first:')
    expect([...document.querySelectorAll('ul li')].map((li) => li.textContent)).toEqual([line])
    expect(document.querySelectorAll('input')).toHaveLength(0)
    const fresh = pricesWith()
    vi.mocked(client.get).mockResolvedValueOnce(fresh)
    await click(button('Check again'))
    expect(vi.mocked(client.get)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices')
    expect(onPrices).toHaveBeenCalledWith(fresh)
  })

  it('says when there’s no hosting plan yet', async () => {
    expect(await render(pricesWith({ plans: [] }))).toContain('Your store has no hosting plans yet.')
    expect(button('Check again')).toBeTruthy()
  })

  it('lists an open store’s prices to change even while a plan needs a fix, and goes back from no plans too', async () => {
    const line = 'Starter and Plus are on the same product. Give each its own product in Whop.'
    const text = await render(pricesWith({ canOpen: false, canUpdate: true, fixable: true, blocked: [line] }), { edit: true })
    expect(field('Starter').value).toBe('15.00')
    expect(button('Save prices')).toBeTruthy()
    expect(text).not.toContain('Fix my plans')
    expect(text).not.toContain(line)
    await click(button('Back'))
    expect(onBack).toHaveBeenCalledTimes(1)
    await render(pricesWith({ plans: [], canOpen: false, canUpdate: true }), { edit: true })
    await click(button('Back'))
    expect(onBack).toHaveBeenCalledTimes(2)
  })

  it('lists only the plans whose price it can set, and saves only those', async () => {
    const yearly = { ...starter, id: 'plan_yearly', title: 'Yearly', settable: false }
    await render(pricesWith({ plans: [yearly, { ...starter, price: 1500 }, plus], canOpen: false, canUpdate: true }), { edit: true })
    expect(document.querySelectorAll('input')).toHaveLength(2)
    await type(field('Plus'), '28')
    vi.mocked(client.post).mockResolvedValueOnce(pricesWith())
    await click(button('Save prices'))
    expect(vi.mocked(client.post)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_plus', price: '28.00' })
  })

  it('changes an open store’s prices with Save prices, and goes back', async () => {
    await render(pricesWith({ plans: [{ ...starter, price: 1500 }, plus], canOpen: false, canUpdate: true }), { edit: true })
    expect(document.querySelector('h1')?.textContent).toBe('Your prices')
    expect(document.querySelector('[aria-current="step"]')).toBeNull()
    await click(button('Back'))
    expect(onBack).toHaveBeenCalledTimes(1)
    await type(field('Plus'), '28')
    vi.mocked(client.post).mockResolvedValueOnce(pricesWith())
    await click(button('Save prices'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_plus', price: '28.00' })
  })
})
