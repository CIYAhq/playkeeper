// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { SellerView } from '@/api/types'
import { formatLongDate } from '@/lib/format'
import { SellerStoreView } from './whop-seller-view'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
}))

let root: Root | undefined

async function render(store: string): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<SellerStoreView store={store} />))
  await act(async () => {})
  return document.body.textContent ?? ''
}

const view = (over: Partial<SellerView> = {}): SellerView => ({
  store: { id: 'biz_other', title: 'Other Hosting', state: 'selling' },
  plans: [],
  customers: [],
  earnings: [],
  ...over,
})

beforeEach(() => {
  vi.mocked(client.get).mockReset()
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
})

describe('a seller’s view of their store', () => {
  it('reads the store through a relative address, and shows its plans, customers and earnings', async () => {
    vi.mocked(client.get).mockResolvedValueOnce(
      view({
        plans: [
          { id: 'plan_other', title: 'Survival', price: '$15.00 / month', servers: 1, memoryMB: 4096, stock: 3, unlimitedStock: false, customers: 2 },
          { id: 'plan_big', title: 'Big', price: '$40.00 / month', servers: 3, memoryMB: 12288, stock: 0, unlimitedStock: true, customers: 0 },
        ],
        customers: [
          { handle: 'alexplays', plan: 'Survival', since: '2026-09-12T10:00:00Z', status: 'active' },
          { handle: 'samcrafts', plan: 'Survival', since: '2026-09-13T10:00:00Z', status: 'suspended' },
          { handle: 'kimbuilds', status: 'ended' },
        ],
        earnings: [
          { month: '2026-09', currency: 'usd', sales: 3000, share: 1700, kept: 1300 },
          { month: '2026-08', currency: 'jpy', sales: 2000, share: 1100, kept: 900 },
        ],
      }),
    )
    const text = await render('biz_other')
    expect(vi.mocked(client.get)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other')
    expect(text).toContain('Your store is selling.')
    expect(text).toContain('Survival$15.00 / month · 1 server with 4 GB · 3 left · 2 customers')
    expect(text).toContain('Big$40.00 / month · 3 servers with 12 GB · no limit · 0 customers')
    expect(text).toContain(`alexplaysSurvival · Active · since ${formatLongDate('2026-09-12T10:00:00Z')}`)
    expect(text).toContain('samcraftsSurvival · Suspended by Playkeeper')
    expect(text).toContain('kimbuildsTheir plan ended')
    expect(text).toContain('September 2026$30.00 in sales · $17.00 to Playkeeper · $13.00 left before Whop’s fees')
    expect(text).toContain('August 2026¥2,000 in sales · ¥1,100 to Playkeeper · ¥900 left before Whop’s fees')
  })

  it('says why a store isn’t selling, without the reason Playkeeper suspended it for', async () => {
    vi.mocked(client.get).mockResolvedValueOnce(view({ store: { id: 'biz_other', title: 'Other Hosting', state: 'closed', why: 'Not open yet' } }))
    expect(await render('biz_other')).toContain('Your store isn’t taking orders.Not open yet')
    vi.mocked(client.get).mockResolvedValueOnce(view({ store: { id: 'biz_other', title: 'Other Hosting', state: 'needsLook', why: 'The grant lacks plan:basic:read.' } }))
    expect(await render('biz_other')).toContain('Your store needs a look.The grant lacks plan:basic:read.')
    vi.mocked(client.get).mockResolvedValueOnce(view({ store: { id: 'biz_other', title: 'Other Hosting', state: 'suspended' } }))
    expect(await render('biz_other')).toContain('Playkeeper has suspended your store, so it isn’t selling and its customers’ servers are stopped. Ask Playkeeper why.')
    vi.mocked(client.get).mockResolvedValueOnce(view({ store: { id: 'biz_other', title: 'Other Hosting', state: 'left', why: 'Playkeeper’s share went unpaid for 72 hours.' } }))
    expect(await render('biz_other')).toContain('Your store left Playkeeper Cloud, so its customers’ servers are stopped.Playkeeper’s share went unpaid for 72 hours.')
  })

  it('says what a new store doesn’t have yet', async () => {
    vi.mocked(client.get).mockResolvedValueOnce(view())
    const text = await render('biz_other')
    expect(text).toContain('No plan gives servers yet.')
    expect(text).toContain('No customers yet.')
    expect(text).toContain('Earnings appear here with your first payment.')
  })

  it('says why the view can’t be read', async () => {
    vi.mocked(client.get).mockRejectedValueOnce(new client.ApiError(403, { code: 'whop_not_team', error: 'Only the business’s team on Whop can open its Playkeeper Cloud page.' }))
    expect(await render('biz_other')).toContain('Only the business’s team on Whop can open its Playkeeper Cloud page.')
  })
})
