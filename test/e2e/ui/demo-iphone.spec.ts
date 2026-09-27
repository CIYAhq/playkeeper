import { devices, expect, test } from '@playwright/test'

// The live demo in Safari on an iPhone, against the site image as in
// demo.spec.ts, with Playwright's WebKit (npx playwright install webkit).
// Safari stops loading the code of a page you leave, and the demo reloaded
// itself then instead of going where you pressed. A new version of the demo,
// put up while it was open, still reloads it.
test.use({ ...devices['iPhone 15'] })

const demoUrl = process.env.PK_DEMO_URL ?? 'http://127.0.0.1:8460/demo/'
const origin = new URL(demoUrl).origin

// No visit is counted from a test run, and the first visit's sheet stays out of the way.
test.beforeEach(async ({ page }) => {
  await page.route('https://analytics-c.ciya.so/**', (route) => route.fulfill({ contentType: 'text/javascript', body: '' }))
  await page.addInitScript(() => localStorage.setItem('playkeeper-demo-welcomed', '1'))
})

test('leaving the demo while its code loads goes where the link points', async ({ page }) => {
  let loads = 0
  page.on('request', (r) => {
    if (r.isNavigationRequest() && r.url() === demoUrl) loads++
  })
  // Once the link shows, the rest of the demo's code loads as slowly as on a bad connection: not before it's pressed.
  let slow = false
  let loading = 0
  await page.route(`${origin}/demo/assets/*.js`, async (route) => {
    if (slow) loading++
    else await route.continue()
  })
  await page.goto(demoUrl)
  const install = page.getByRole('link', { name: 'Install on your VPS' })
  await expect(install).toBeVisible()
  slow = true
  await expect.poll(() => loading, { message: 'the demo loads more of its code' }).toBeGreaterThan(0)
  await install.tap()
  await expect(page).toHaveURL(`${origin}/#install`)
  await expect(page.locator('#install')).toBeVisible()
  expect(loads, 'the demo didn’t reload').toBe(1)
})

test('a new version of the demo, put up while it was open, reloads it once', async ({ page }) => {
  // The open version had loaded only the files its page names when the new
  // one replaced the rest, and the demo's page now starts from another script.
  const html = await (await page.request.get(demoUrl)).text()
  const own = new Set([...html.matchAll(/="(\/demo\/assets\/[^"]+\.js)"/g)].map(([, file]) => origin + file))
  const entry = /<script type="module"[^>]* src="([^"]+)"/.exec(html)?.[1] ?? ''
  expect(own).toContain(origin + entry)
  let loads = 0
  await page.route(demoUrl, async (route) => {
    if (!route.request().isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: html.replace(entry, '/demo/assets/index-N3wVers1.js') })
    loads++
    await route.continue()
  })
  await page.route(`${origin}/demo/assets/*.js`, (route) => (own.has(route.request().url()) ? route.continue() : route.fulfill({ status: 404 })))
  await page.goto(demoUrl)
  await expect.poll(() => loads, { message: 'the demo reloads' }).toBe(2)
  // Its code still doesn't load after that, so it says so instead of reloading again.
  await expect(page.getByRole('alert')).toContainText('Couldn’t load this page')
  expect(loads).toBe(2)
})
