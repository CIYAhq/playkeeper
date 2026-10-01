import { describe, expect, it, vi } from 'vitest'
import { createHandler, fresh, stale } from '../src/handler.ts'
import { escape, html } from '../src/html.ts'
import { playkeeperCloudApp } from '../src/whop.ts'
import worker from '../src/worker.ts'
import { dashboard, fakeWhop, freshCopy, openStore, type Catalogue } from './fake-whop.ts'

const env = { WHOP_API_ORIGIN: 'https://api.example.test', WHOP_ACCOUNT_ID: 'biz_pip' }

/** A store as Whop hosting runs it for the catalogue's business. */
function store(catalogue: () => Catalogue, opts: { down?: () => boolean; env?: Record<string, string> } = {}) {
  const whop = fakeWhop(catalogue, opts)
  let now = 1_000_000
  const handle = createHandler({ fetch: whop.fetch, now: () => now })
  const mine = { ...env, WHOP_ACCOUNT_ID: String(catalogue().account.id), ...opts.env }
  return {
    whop,
    get: (path: string, init?: RequestInit) => handle(new Request(`https://pip-hosting.whop.site${path}`, init), mine),
    later: (ms: number) => (now += ms),
  }
}

const text = (html: string) => html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

/** The setup page's steps, each as its title, its sentence, its button's link and its button's label. */
const stepsOf = (page: string) =>
  [...page.matchAll(/<li>\n<h2>(.*?)<\/h2>\n<p>(.*?)<\/p>\n<a class="btn btn-primary" href="(.*?)">(.*?)<\/a>\n<\/li>/g)].map((m) => m.slice(1, 5))

describe('markup', () => {
  it('escapes what it puts in, apart from other markup', () => {
    expect(escape(`<a href="x">'&'</a>`)).toBe('&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;')
    expect(html`<p title="${'"x"'}">${['<b>', html`<i>i</i>`]}${null}${undefined}${false}${0}</p>`.value).toBe('<p title="&quot;x&quot;">&lt;b&gt;<i>i</i>0</p>')
  })
})

describe('the store’s pages', () => {
  it('shows the plans on sale with Whop’s checkout, and the dashboard to sign in to', async () => {
    const s = store(openStore)
    const res = await s.get('/')
    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toBe('text/html; charset=utf-8')
    expect(res.headers.get('cache-control')).toBe('public, max-age=60')
    const page = await res.text()
    expect(page.startsWith('<!doctype html>\n<html lang="en">')).toBe(true)
    expect(page).toContain('<title>Pip Hosting: Minecraft server hosting</title>')
    expect(page).toContain('<a class="btn btn-primary" href="https://whop.com/checkout/plan_starter">Choose Starter</a>')
    expect(page).toContain('<a class="btn btn-primary" href="https://whop.com/checkout/plan_plus">Choose Plus</a>')
    expect(page).toContain(`<a class="btn btn-outline btn-sm" href="${dashboard}/">Sign in</a>`)
    const words = text(page)
    expect(words).toContain('Starter $10.00 / month 3-day free trial 1 server 4 GB of memory')
    expect(words).toContain('Plus $20.00 / month Up to 2 servers 8 GB of memory between them')
    expect(words).toContain('Pip Hosting tells you in your Whop messages when your server is ready.')
    expect(words).toContain('sign in with your Whop account')
    expect(page.toLowerCase()).not.toContain('invite')
    expect(words).toContain('Not approved by or associated with Mojang or Microsoft.')
    for (const hidden of ['plan_test', 'Owner test', 'plan_old', 'plan_link', 'plan_course', 'Server growth course']) expect(page).not.toContain(hidden)
    expect(page).not.toContain('Not taking orders yet')
  })

  it('tells visitors a fresh copy of the blueprint isn’t taking orders yet, at the top, with one way in for its owner', async () => {
    const page = await (await store(freshCopy).get('/')).text()
    expect(text(page)).toContain('Not taking orders yet Joe’s Hosting opens here soon. Is this your store? Set up my store')
    expect(page).toContain('<a class="btn btn-primary" href="/setup">Set up my store</a>')
    expect(page.indexOf('class="closed"')).toBeLessThan(page.indexOf('How it works'))
    expect(page).not.toContain('id="plans-title"')
    expect(page).not.toContain('whop.com/checkout')
    expect(page).not.toContain('whop.com/apps/')
    expect(page).not.toContain('oauth:token_exchange')
    expect(page).not.toMatch(/>Sign in( to your dashboard)?<\/a>/)
    expect(page).not.toContain('See the plans')
  })

  it('walks a fresh copy’s owner through connecting Playkeeper Cloud, one sentence and one button a step', async () => {
    const res = await store(freshCopy).get('/setup')
    expect(res.status).toBe(200)
    expect(res.headers.get('cache-control')).toBe('public, max-age=60')
    const page = await res.text()
    expect(page).toContain('<title>Set up your store: Joe’s Hosting</title>')
    expect(page).toContain('<meta name="robots" content="noindex">')
    expect(stepsOf(page)).toEqual([
      ['Connect Playkeeper Cloud', 'Pick Joe’s Hosting and approve it.', `https://whop.com/apps/${playkeeperCloudApp}/install`, 'Connect Playkeeper Cloud'],
      ['Open Playkeeper Cloud', 'Find it under Apps in your Whop dashboard.', 'https://whop.com/dashboard/biz_copy', 'Open my dashboard'],
      ['Open your store', 'In Playkeeper Cloud, press Open the store.', '/', 'See my store'],
    ])
    expect(page).toContain('href="https://playkeeper.io/guides/start-a-minecraft-hosting-company"')
    expect(page).not.toContain('whop.com/checkout')
    expect((await (await store(freshCopy).get('/')).text())).not.toContain('<meta name="robots"')
    const other = await (await store(freshCopy, { env: { PLAYKEEPER_CLOUD_APP: 'app_other0123' } }).get('/setup')).text()
    expect(stepsOf(other)[0]?.[2]).toBe('https://whop.com/apps/app_other0123/install')
  })

  it('walks the owner of a store that connects its own Playkeeper through its three steps', async () => {
    for (const setting of ['off', 'app_x"><script>']) {
      const s = store(freshCopy, { env: { PLAYKEEPER_CLOUD_APP: setting } })
      expect(text(await (await s.get('/')).text())).toContain('Not taking orders yet Joe’s Hosting opens here soon. Is this your store? Set up my store')
      const page = await (await s.get('/setup')).text()
      expect(stepsOf(page)).toEqual([
        ['Install Playkeeper', 'Put it on your own server.', 'https://playkeeper.io', 'Get Playkeeper'],
        ['Connect this store', 'Paste a Whop API key in Playkeeper’s Settings › Sell on Whop.', 'https://whop.com/dashboard/developer', 'Get an API key'],
        ['Show your plans', 'Make your plans visible on Whop.', 'https://whop.com/dashboard/biz_copy', 'Open my dashboard'],
      ])
      expect(page).not.toContain('Connect Playkeeper Cloud')
      expect(page).not.toContain('<script>')
    }
  })

  it('sends the owner of a store taking orders from the setup steps to its plans', async () => {
    const s = store(openStore)
    const res = await s.get('/setup/')
    expect(res.status).toBe(302)
    expect(res.headers.get('location')).toBe('/')
    expect(await (await s.get('/')).text()).not.toContain('Set up my store')
  })

  it('links the setup steps to Whop’s dashboard itself when Whop names the business oddly', async () => {
    const page = await (await store(() => {
      const c = freshCopy()
      c.account = { ...c.account, id: 'biz_x"><b>', title: '' }
      return c
    }).get('/setup')).text()
    expect(stepsOf(page)[0]?.[1]).toBe('Pick this business and approve it.')
    expect(stepsOf(page)[1]?.[2]).toBe('https://whop.com/dashboard')
    expect(page).not.toContain('<b>')
  })

  it('shows a store whose products each sell one plan as one row of plans, cheapest first', async () => {
    const page = await (await store(() => {
      const c = openStore()
      c.products.push({ id: 'prod_big', title: 'Big server', headline: null, visibility: 'visible', metadata: { playkeeper_dashboard: dashboard, playkeeper_business: 'biz_pip' } })
      c.plans = c.plans.map((p) => (p.id === 'plan_plus' ? { ...p, product: { id: 'prod_big', title: 'Big server' } } : p))
      return c
    }).get('/')).text()
    expect(page).not.toContain('shelf-title')
    expect(page.match(/<ul class="plans">/g)).toHaveLength(1)
    expect(page).toContain('<h3 id="offer-plan_starter">Starter</h3>')
    expect(page.indexOf('Choose Starter')).toBeLessThan(page.indexOf('Choose Plus'))
  })

  it('escapes whatever the business put in its names', async () => {
    const hostile = '<script>alert(1)</script>'
    const page = await (await store(() => {
      const c = openStore()
      c.account = { ...c.account, title: `Pip ${hostile}`, description: `"><img src=x onerror=alert(1)>` }
      c.plans = c.plans.map((p) => ({ ...p, title: hostile, description: hostile, formatted_price: hostile }))
      return c
    }).get('/')).text()
    expect(page).not.toContain('<script>')
    expect(page).not.toContain('<img src=x')
    expect(page).toContain('&lt;script&gt;alert(1)&lt;/script&gt;')
    const setup = await (await store(() => {
      const c = freshCopy()
      c.account = { ...c.account, title: `Joe ${hostile}` }
      return c
    }).get('/setup')).text()
    expect(setup).not.toContain('<script>')
    expect(stepsOf(setup)[0]?.[1]).toBe('Pick Joe &lt;script&gt;alert(1)&lt;/script&gt; and approve it.')
  })

  it('has terms with the business’s name, or links to the business’s own', async () => {
    const res = await store(openStore).get('/terms')
    expect(res.status).toBe(200)
    const words = text(await res.text())
    expect(words).toContain('Terms of service')
    expect(words).toContain('Your servers stop, and you can still sign in to download their backups.')
    expect(words).toContain('After 14 days your servers are deleted. A final backup of each is kept for 30 days, then deleted for good.')
    expect(words).toContain('Pip Hosting runs the servers as well as it can, with no promise of uptime.')
    const own = await (await store(() => {
      const c = openStore()
      c.account = { ...c.account, terms_of_service: { url: 'https://assets.whop.com/pip-terms.pdf' } }
      return c
    }).get('/')).text()
    expect(own).toContain('<a href="https://assets.whop.com/pip-terms.pdf">Terms</a>')
  })

  it('answers other paths with a 404, and other methods with a 405', async () => {
    const s = store(openStore)
    const missing = await s.get('/wp-admin')
    expect(missing.status).toBe(404)
    expect(text(await missing.text())).toContain('No page here')
    expect((await s.get('/terms/')).status).toBe(200)
    const post = await s.get('/', { method: 'POST' })
    expect(post.status).toBe(405)
    expect(post.headers.get('allow')).toBe('GET, HEAD')
  })
})

describe('reading Whop', () => {
  it('reads the store once a minute, not for every visitor', async () => {
    const s = store(openStore)
    await s.get('/')
    await s.get('/terms')
    s.later(fresh - 1)
    await s.get('/')
    expect(s.whop.seen.filter((x) => x.url.pathname.endsWith('/accounts/me'))).toHaveLength(1)
    s.later(1)
    await s.get('/')
    expect(s.whop.seen.filter((x) => x.url.pathname.endsWith('/accounts/me'))).toHaveLength(2)
  })

  it('shows what it last read for an hour while Whop can’t be reached, then says the store is back soon', async () => {
    let down = false
    const s = store(openStore, { down: () => down })
    await s.get('/')
    down = true
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    s.later(fresh)
    const cached = await s.get('/')
    expect(cached.status).toBe(200)
    expect(await cached.text()).toContain('Choose Starter')
    s.later(stale - fresh)
    const gone = await s.get('/')
    expect(gone.status).toBe(503)
    expect(gone.headers.get('cache-control')).toBe('no-store')
    expect(text(await gone.text())).toContain('The store couldn’t load its plans from Whop just now.')
    expect(errors).toHaveBeenCalledWith('Reading the store from Whop failed: /accounts/me: Whop is down')
    errors.mockRestore()
  })

  it('asks Whop at most once a minute while it can’t be reached, showing what it last read meanwhile', async () => {
    let down = false
    const s = store(openStore, { down: () => down })
    const reads = () => s.whop.seen.filter((x) => x.url.pathname.endsWith('/accounts/me')).length
    await s.get('/')
    down = true
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    s.later(fresh)
    await s.get('/')
    expect(reads()).toBe(2)
    s.later(fresh - 1)
    for (const path of ['/', '/terms', '/']) expect((await s.get(path)).status).toBe(200)
    expect(reads()).toBe(2)
    s.later(1)
    expect(await (await s.get('/')).text()).toContain('Choose Starter')
    expect(reads()).toBe(3)
    down = false
    s.later(fresh)
    await s.get('/')
    expect(reads()).toBe(4)
    errors.mockRestore()
  })

  it('waits a minute before asking Whop again after a first read failed', async () => {
    const s = store(openStore, { down: () => true })
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect((await s.get('/')).status).toBe(503)
    s.later(fresh - 1)
    expect((await s.get('/')).status).toBe(503)
    expect(s.whop.seen).toHaveLength(1)
    s.later(1)
    await s.get('/')
    expect(s.whop.seen).toHaveLength(2)
    errors.mockRestore()
  })

  it('exports nothing but the handler from the Worker’s module, as the Workers runtime takes every named export for an entrypoint', async () => {
    expect(Object.keys(await import('../src/worker.ts'))).toEqual(['default'])
  })

  it('reads the settings Whop hosting gives the Worker', async () => {
    const whop = fakeWhop(openStore)
    const real = globalThis.fetch
    globalThis.fetch = whop.fetch
    try {
      const res = await worker.fetch(new Request('https://pip-hosting.whop.site/'), env)
      expect(res.status).toBe(200)
      expect(whop.seen[0]?.url.href).toBe('https://api.example.test/api/v1/accounts/me')
    } finally {
      globalThis.fetch = real
    }
  })
})
