// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { SellerCustomer, SellerView } from '@/api/types'
import { LiveView } from './whop-seller-view'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  post: vi.fn(() => new Promise(() => {})),
}))

let root: Root | undefined
const onChangePrices = vi.fn()
const onChange = vi.fn()

async function render(view: SellerView, route?: string): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<LiveView store="biz_other" route={route} view={view} onChangePrices={onChangePrices} onChange={onChange} />))
  return document.body.textContent ?? ''
}

const viewWith = (over: Partial<SellerView> = {}): SellerView => ({
  store: { id: 'biz_other', title: 'Other Hosting', state: 'selling' },
  plans: [],
  customers: [],
  earnings: [],
  ...over,
})

function button(text: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === text)
  if (!b) throw new Error(`no button ${text}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

beforeEach(() => {
  vi.mocked(client.post).mockReset()
  onChangePrices.mockReset()
  onChange.mockReset()
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
})

describe('a seller’s open store', () => {
  it('says it’s live, links to the store, and shows what it earned, newest first, and its customers, briefly', async () => {
    const customers: SellerCustomer[] = ['ana', 'ben', 'cal', 'dee', 'eve', 'fin', 'gus'].map((handle) => ({ handle, plan: 'Starter', status: 'active', since: '2026-09-20T10:00:00Z' }))
    customers[1] = { handle: 'ben', plan: 'Starter', status: 'starting' }
    const text = await render(
      viewWith({
        customers,
        earnings: [
          { month: '2026-07', currency: 'usd', sales: 1500, share: 850, kept: 650 },
          { month: '2026-09', currency: 'usd', sales: 4500, share: 2550, kept: 1950 },
          { month: '2026-08', currency: 'usd', sales: 3000, share: 1700, kept: 1300 },
          { month: '2026-06', currency: 'usd', sales: 1500, share: 850, kept: 650 },
        ],
      }),
      'other-hosting',
    )
    expect(document.querySelector('h1')?.textContent).toBe('You’re live')
    const visit = [...document.querySelectorAll('a')].find((a) => a.textContent?.includes('Visit your store'))
    expect(visit?.getAttribute('href')).toBe('https://whop.com/other-hosting')
    expect(visit?.getAttribute('aria-label')).toContain('Visit your store')
    const months = [...(document.querySelector('section')?.querySelectorAll('li') ?? [])].map((li) => li.textContent)
    expect(months).toEqual(['September 2026$19.50', 'August 2026$13.00', 'July 2026$6.50'])
    expect(text).toContain('7 customers')
    expect(text).toContain('anaActive')
    expect(text).toContain('benSetting up')
    expect(text).not.toContain('fin')
    expect(text).toContain('and 2 more')
  })

  it('says when there’s nothing to show yet, and what a store that needs a look needs', async () => {
    const text = await render(viewWith({ store: { id: 'biz_other', title: 'Other Hosting', state: 'needsLook', why: 'Starter and Plus are on the same product.' } }))
    expect(text).toContain('No sales yet.')
    expect(text).toContain('CustomersNo customers yet.')
    expect(text).not.toContain('0 customers')
    expect(text).toContain('Starter and Plus are on the same product.')
    expect(document.querySelector('a')).toBeNull()
  })

  it('updates the store for a plan added since, and says so or why not', async () => {
    await render(viewWith())
    vi.mocked(client.post).mockResolvedValueOnce({ open: true })
    await click(button('Update the store'))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/sell')
    expect(document.querySelector('[role="status"]')?.textContent).toBe('Your store is up to date.')
    expect(onChange).toHaveBeenCalledTimes(1)
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(409, { code: 'conflict', error: 'Big: doesn’t renew every month\nHuge: allows 96 GB', hint: 'To update your store, allow 1 to 10 servers and 1 to 64 GB and renew every month.' }),
    )
    await click(button('Update the store'))
    const alert = document.querySelector('[role="alert"]')
    expect([...(alert?.querySelectorAll('p') ?? [])].map((p) => p.textContent)).toEqual([
      'Big: doesn’t renew every month',
      'Huge: allows 96 GB',
      'To update your store, allow 1 to 10 servers and 1 to 64 GB and renew every month.',
    ])
  })

  it('opens the prices to change them', async () => {
    await render(viewWith())
    await click(button('Change prices'))
    expect(onChangePrices).toHaveBeenCalledTimes(1)
  })
})
