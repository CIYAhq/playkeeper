// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import { href, parse } from '@/lib/router'
import { WhopSellerPage } from './whop-seller'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
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

beforeEach(() => {
  vi.mocked(client.post).mockReset()
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

  it('opens the store through a relative address, and says it’s connected', async () => {
    vi.mocked(client.post).mockResolvedValueOnce({ store: { id: 'biz_other', title: 'Other Hosting' }, new: true })
    const text = await render('biz_other')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/public/whop/seller/biz_other/open')
    expect(text).toContain('Other Hosting is connected. Playkeeper Cloud runs its customers’ servers.')
    expect(text).toContain('Next, you’ll set your prices and open the store here.')
  })

  it('says what a store that’s already there needs', async () => {
    vi.mocked(client.post).mockResolvedValueOnce({ store: { id: 'biz_other', title: 'Other Hosting', problem: 'The grant lacks plan:basic:read.' }, new: false })
    const text = await render('biz_other')
    expect(text).toContain('Other Hosting is connected to Playkeeper Cloud.It needs a look: The grant lacks plan:basic:read.')
  })

  it('sends a business that hasn’t approved the app to approve it, picking this business', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(409, {
        code: 'whop_not_approved',
        error: 'This business hasn’t approved everything Playkeeper Cloud asks for.',
        params: { installUrl: 'https://whop.com/apps/app_6oyNYgGluUMTx4/install', lacking: ['plan:basic:read'] },
      }),
    )
    const text = await render('biz_other')
    expect(text).toContain('pick this business in Whop’s business picker, and approve')
    const link = [...document.querySelectorAll('a')].find((a) => a.textContent?.includes('Approve Playkeeper Cloud for this business'))
    expect(link?.getAttribute('href')).toBe('https://whop.com/apps/app_6oyNYgGluUMTx4/install')
    expect(link?.getAttribute('target')).toBe('_blank')
  })

  it('refuses someone who isn’t on the business’s team, and a page opened outside Whop', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(403, { code: 'whop_not_team', error: 'Only the business’s team…' }))
    expect(await render('biz_other')).toContain('Only this business’s team on Whop can open its Playkeeper Cloud page.')
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(401, { code: 'whop_token', error: 'This page opens inside…' }))
    expect(await render('biz_other')).toContain('Open this page from your Whop dashboard, which tells Playkeeper who you are.')
  })

  it('calls nothing for an address that can’t be a business’s page', async () => {
    expect(await render('')).toContain('This address isn’t a business’s Playkeeper Cloud page.')
    expect(vi.mocked(client.post)).not.toHaveBeenCalled()
  })
})
