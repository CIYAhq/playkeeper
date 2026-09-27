import { expect, test, type Page } from '@playwright/test'
import { FakeConsole, fakePanel, server } from './fake-panel'

// Files dropped on the Files tab on a desktop, or picked with Upload files on
// a phone, go to the machine in pieces through the panel. The fake machine
// drops the connection partway through the larger file: the upload asks where
// it stands and carries on from the byte the machine has, as world uploads do,
// and the folder shows both files once they're in place.

const sizes = [
  { name: 'desktop', viewport: { width: 1440, height: 900 }, phone: false },
  { name: 'phone', viewport: { width: 390, height: 844 }, phone: true },
]

const at = '2026-09-26T21:04:12Z'
const big = { name: 'Chunky-1.5.3.jar', mimeType: 'application/java-archive', buffer: Buffer.alloc(3 << 20, 7) }
const small = { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('Build night on Friday\n') }
/** Where the connection drops the first time the larger file is sent. */
const dropAt = 1 << 20

interface Upload {
  id: string
  folder: string
  createdAt: string
  files: { index: number; name: string; size: number; received: number; placed?: boolean }[]
  limitBytes: number
}

/** The machine's side of the Files tab: the server's folder, and uploads into it that keep what arrives. */
async function fakeFiles(page: Page) {
  const s = server()
  const base = `/api/servers/${s.id}/files`
  const seen = { pieces: [] as string[], statusReads: 0, deleted: [] as string[], lists: 0, dropped: 0 }
  const uploads = new Map<string, Upload>()
  const folder = [
    { name: 'plugins', type: 'folder', size: 0, modifiedAt: at },
    { name: 'server.properties', type: 'file', size: 1210, modifiedAt: at },
  ]
  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const method = req.method()
    const placed = [...uploads.values()].flatMap((u) => u.files.filter((f) => f.placed).map((f) => ({ name: f.name, type: 'file', size: f.size, modifiedAt: at })))
    if (method === 'GET' && url.pathname === base) {
      seen.lists++
      return route.fulfill({ json: { path: url.searchParams.get('path') ?? '', entries: [...folder, ...placed], running: true, worlds: ['world', 'world_nether', 'world_the_end'] } })
    }
    if (method === 'POST' && url.pathname === `${base}/uploads`) {
      const up: Upload = { id: `${uploads.size + 1}`.padStart(16, '0'), folder: (req.postDataJSON() as { folder: string }).folder, createdAt: at, files: [], limitBytes: 2 ** 34 }
      uploads.set(up.id, up)
      return route.fulfill({ status: 201, json: up })
    }
    const m = /\/files\/uploads\/([0-9a-f]{16})(\/files(?:\/(\d+))?)?$/.exec(url.pathname)
    const up = m && uploads.get(m[1] ?? '')
    if (!m || !up) return route.fallback()
    if (method === 'GET' && !m[2]) {
      seen.statusReads++
      return route.fulfill({ json: up })
    }
    if (method === 'DELETE' && !m[2]) {
      seen.deleted.push(up.id)
      return route.fulfill({ status: 204, body: '' })
    }
    if (method === 'POST' && m[2] && !m[3]) {
      const b = req.postDataJSON() as { name: string; size: number; replace: boolean }
      up.files.push({ index: up.files.length, name: b.name, size: b.size, received: 0, placed: b.size === 0 })
      return route.fulfill({ status: 201, json: up })
    }
    if (method === 'PUT' && m[3]) {
      const f = up.files[Number(m[3])]
      const offset = Number(url.searchParams.get('offset'))
      if (!f) return route.fulfill({ status: 404, json: { error: 'File not found.', code: 'not_found' } })
      seen.pieces.push(`${f.name} from ${offset}`)
      if (offset !== f.received) return route.fulfill({ status: 409, json: { error: `The upload carries on from byte ${f.received}.`, code: 'conflict' } })
      const body = req.postDataBuffer() ?? Buffer.alloc(0)
      if (f.name === big.name && seen.dropped === 0) {
        seen.dropped++
        f.received += Math.min(body.length, dropAt)
        return route.abort('connectionreset')
      }
      f.received += body.length
      f.placed = f.received === f.size
      return route.fulfill({ json: up })
    }
    return route.fallback()
  })
  return { seen, uploads }
}

/** Drops files on the folder the way a browser does, from a DataTransfer with the files in it. */
async function drop(page: Page, files: { name: string; mimeType: string; buffer: Buffer }[]) {
  const data = await page.evaluateHandle((list) => {
    const dt = new DataTransfer()
    for (const f of list) dt.items.add(new File([new Uint8Array(f.bytes)], f.name, { type: f.mimeType }))
    return dt
  }, files.map((f) => ({ name: f.name, mimeType: f.mimeType, bytes: [...f.buffer] })))
  const target = page.locator('section[aria-labelledby="files-folder"]')
  await target.dispatchEvent('dragenter', { dataTransfer: data })
  await expect(page.getByText('Drop to upload to the server’s folder')).toBeVisible()
  await target.dispatchEvent('dragover', { dataTransfer: data })
  await target.dispatchEvent('drop', { dataTransfer: data })
}

for (const size of sizes) {
  test.describe(size.name, () => {
    test.use({ viewport: size.viewport, isMobile: size.phone, hasTouch: size.phone })

    test('several files go up in pieces and carry on after the connection drops', async ({ page }) => {
      const { server: s, unexpected } = await fakePanel(page, new FakeConsole())
      const { seen, uploads } = await fakeFiles(page)
      await page.goto(`/servers/${s.slug}/files`)
      await expect(page.getByText('server.properties')).toBeVisible()

      if (size.phone) await page.getByLabel('Files to upload').setInputFiles([big, small])
      else await drop(page, [big, small])

      await expect(page.getByText('Connection lost. Trying again…')).toBeVisible()
      await expect(page.getByText('Uploaded 2 files to the server’s folder')).toBeVisible({ timeout: 30_000 })
      expect(seen.pieces).toEqual([`${big.name} from 0`, `${big.name} from ${dropAt}`, `${small.name} from 0`])
      expect(seen.statusReads).toBeGreaterThan(0)
      const up = [...uploads.values()][0]
      expect(up?.folder).toBe('')
      expect(up?.files.map((f) => [f.name, f.received, f.placed])).toEqual([
        [big.name, big.buffer.length, true],
        [small.name, small.buffer.length, true],
      ])
      // Once every file is in place, the machine's upload is deleted once and the folder shows the files.
      await expect.poll(() => seen.deleted).toEqual([up?.id])
      await expect(page.getByText(big.name)).toBeVisible()
      await expect(page.getByText(small.name)).toBeVisible()
      await page.getByRole('button', { name: 'Dismiss' }).click()
      await expect(page.getByText('Uploaded 2 files to the server’s folder')).toHaveCount(0)
      expect(seen.deleted).toHaveLength(1)
      expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
    })
  })
}
