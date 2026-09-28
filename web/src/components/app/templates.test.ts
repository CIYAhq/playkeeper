import { afterEach, expect, it, vi } from 'vitest'

// Browse templates hands playkeeper.io this dashboard's address after #, which
// the site keeps in the browser for its Open in my dashboard; the live demo has
// no dashboard to come back to.
afterEach(() => {
  vi.doUnmock('@/lib/demo')
  vi.resetModules()
})

it("gives playkeeper.io this dashboard's address after #, which never reaches a server", async () => {
  const { browseTemplatesURL } = await import('@/components/app/templates')
  expect(browseTemplatesURL('https://siya.playkeeper.me:8443')).toBe('https://playkeeper.io/t#dashboard=https%3A%2F%2Fsiya.playkeeper.me%3A8443')
  expect(browseTemplatesURL('https://203.0.113.7:8443')).toBe('https://playkeeper.io/t#dashboard=https%3A%2F%2F203.0.113.7%3A8443')
})

it('links the directory alone from the live demo', async () => {
  vi.doMock('@/lib/demo', () => ({ demo: { templates: false } }))
  const { browseTemplatesURL } = await import('@/components/app/templates')
  expect(browseTemplatesURL('https://playkeeper.io')).toBe('https://playkeeper.io/templates')
})
