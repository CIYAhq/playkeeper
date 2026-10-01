// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { SellerPrices } from '@/api/types'
import { SellerPricesCard } from './whop-seller-prices'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => new Promise(() => {})),
}))

let root: Root | undefined
const changed = vi.fn()

async function render(prices: SellerPrices): Promise<string> {
  vi.mocked(client.get).mockResolvedValueOnce(prices)
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<SellerPricesCard store="biz_other" onChange={changed} />))
  await act(async () => {})
  return document.body.textContent ?? ''
}

const starter = { id: 'plan_other', title: 'Starter', servers: 1, memoryMB: 4096, price: 1200, currency: 'usd', floor: 1200, share: 850, settable: true }
const pricesWith = (over: Partial<SellerPrices> = {}): SellerPrices => ({ plans: [starter], canOpen: true, canUpdate: false, ...over })

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
  changed.mockReset()
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
})

describe('a seller’s prices', () => {
  it('lists each hosting plan at its price, with the floor and Playkeeper’s share, from a relative address', async () => {
    const euro = { id: 'plan_euro', title: 'Euro', servers: 1, memoryMB: 4096, price: 1400, currency: 'eur', floor: 1200, share: 850, settable: false, problem: 'It’s priced in EUR, and hosted plans are priced in US dollars.' }
    const text = await render(pricesWith({ plans: [starter, euro] }))
    expect(vi.mocked(client.get)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices')
    expect(text).toContain('Starter1 server with 4 GB · at least $12.00 · $8.50 to Playkeeper')
    expect(field('Starter').value).toBe('12.00')
    expect(text).toContain('€14.00')
    expect(text).toContain('It’s priced in EUR, and hosted plans are priced in US dollars.')
    expect(document.querySelectorAll('input')).toHaveLength(1)
  })

  it('saves a new price through a relative address, and tells the page', async () => {
    await render(pricesWith())
    expect(button('Save').disabled).toBe(true)
    await type(field('Starter'), '15')
    vi.mocked(client.post).mockResolvedValueOnce(pricesWith({ plans: [{ ...starter, price: 1500 }] }))
    await click(button('Save'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/prices', { plan: 'plan_other', price: '15' })
    expect(field('Starter').value).toBe('15.00')
    expect(document.body.textContent).toContain('Saved')
    expect(changed).toHaveBeenCalledTimes(1)
  })

  it('says why a price was refused', async () => {
    await render(pricesWith())
    await type(field('Starter'), '11')
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(400, { code: 'invalid_request', error: 'Starter allows 4 GB, so it charges at least $12.00 a month.', field: 'price' }),
    )
    await click(button('Save'))
    expect(document.body.textContent).toContain('Starter allows 4 GB, so it charges at least $12.00 a month.')
    expect(field('Starter').getAttribute('aria-invalid')).toBe('true')
    expect(changed).not.toHaveBeenCalled()
  })

  it('opens the store through a relative address, then tells the page', async () => {
    await render(pricesWith())
    vi.mocked(client.post).mockResolvedValueOnce({ open: true })
    await click(button('Open the store'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/sell')
    const text = document.body.textContent ?? ''
    expect(text).toContain('Your store is open.')
    expect(text).not.toContain('Open the store')
    expect(button('Update the store')).toBeTruthy()
    expect(changed).toHaveBeenCalledTimes(1)
  })

  it('updates an open store through the same address, then tells the page', async () => {
    const text = await render(pricesWith({ canOpen: false, canUpdate: true }))
    expect(text).not.toContain('Open the store')
    expect(text).toContain('Added a hosting plan on Whop, or changed one?')
    vi.mocked(client.post).mockResolvedValueOnce({ open: true })
    await click(button('Update the store'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/sell')
    expect(document.body.textContent).toContain('Your store is up to date.')
    expect(changed).toHaveBeenCalledTimes(1)
  })

  it('says why an open store wasn’t updated', async () => {
    await render(pricesWith({ canOpen: false, canUpdate: true }))
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(409, { code: 'conflict', error: 'Cheap: It charges $9.00, under the $12.00 floor for 4 GB.' }))
    await click(button('Update the store'))
    expect(document.body.textContent).toContain('Cheap: It charges $9.00, under the $12.00 floor for 4 GB.')
    expect(button('Update the store')).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
  })

  it('says what keeps the store closed', async () => {
    await render(pricesWith())
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(409, { code: 'conflict', error: 'Playkeeper Cloud doesn’t take its share yet: the owner hasn’t said who receives it in Settings › Sell on Whop.' }),
    )
    await click(button('Open the store'))
    expect(document.body.textContent).toContain('Playkeeper Cloud doesn’t take its share yet')
    expect(button('Open the store')).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
    vi.mocked(client.post).mockResolvedValueOnce({ open: false, why: 'Playkeeper’s share on Minecraft server was removed.' })
    await click(button('Open the store'))
    expect(document.body.textContent).toContain('Playkeeper’s share is set, but your store is still closed: Playkeeper’s share on Minecraft server was removed.')
  })

  it('offers no Open the store once the store is open, and says when there’s no hosting plan', async () => {
    const text = await render(pricesWith({ plans: [], canOpen: false }))
    expect(text).not.toContain('Open the store')
    expect(text).toContain('Your store has no hosting plan yet.')
  })
})
