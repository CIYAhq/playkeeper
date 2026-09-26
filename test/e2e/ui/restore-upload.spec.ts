import { expect, test, type Page } from '@playwright/test'
import { FakeConsole, fakePanel, machine, server } from './fake-panel'

// A backup file chosen in the browser is sent to the panel, from the World tab
// (its card on a desktop, its sheet on a phone) and as a new server. The page
// checks only the file's size before sending it; the fake answers without
// reading it.

const sizes = [
  { name: 'desktop', viewport: { width: 1440, height: 900 }, phone: false },
  { name: 'phone', viewport: { width: 390, height: 844 }, phone: true },
]

const file = { name: 'world.tar.gz', mimeType: 'application/gzip', buffer: Buffer.from('a small backup file') }

const backup = {
  id: '20260925-080000-a1b2c3',
  serverId: server().id,
  kind: 'manual',
  createdAt: '2026-09-25T08:00:00Z',
  fileName: 'survival-20260925-080000-a1b2c3.tar.gz',
  sizeBytes: 48 * 2 ** 20,
  sha256: 'ab'.repeat(32),
  location: 'on-host',
  verified: true,
  verifiedAt: '2026-09-25T08:01:00Z',
  downtimeMs: 2400,
  minecraftVersion: '26.1.2',
  levelName: 'world',
  fileCount: 89,
  createdBy: 'siya',
}

const catalog = {
  type: 'paper',
  types: [{ id: 'paper', name: 'Paper', available: true }],
  versions: [
    { id: 'paper-26.1.2-74', label: '26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, jarSha256: 'cd'.repeat(32), java: 25, recommended: true, notes: '', channel: 'STABLE', experimental: false, supported: true },
  ],
  memoryOptionsMB: [1024, 1536, 2048, 3072, 4096],
  recommendedMemoryMB: 2048,
  hostMemoryMB: machine.live.memoryTotalMB,
  maxMemoryMB: 8192,
  systemReserveMB: machine.live.systemReserveMB,
  memoryFreeMB: machine.live.memoryFreeMB,
  servers: [{ id: server().id, name: 'Survival', memoryMB: 4096, running: true }],
  suggestedPort: 25566,
  image: 'itzg/minecraft-server:2026.9.0-java25',
}

function preview(serverId?: string) {
  return {
    id: 'r2345abcde',
    serverId,
    source: `Uploaded file ${file.name}`,
    receivedAt: new Date().toISOString(),
    sizeBytes: file.buffer.length,
    sha256: 'ef'.repeat(32),
    compatible: true,
    problems: [],
    warnings: [],
    currentWorld: serverId ? { exists: true, levelName: 'world', sizeBytes: 11_000_000 } : { exists: false, sizeBytes: 0 },
    willCreateRollback: !!serverId,
    needsEula: !serverId,
    memoryMB: 2048,
    confirmPhrase: serverId ? 'replace world' : 'restore',
    steps: ['Put the backup’s world in place', 'Start the server'],
    notRestored: [],
  }
}

/** Answers the World tab's and the new-server page's reads and records every upload; the rest goes to fakePanel. */
async function fakeRestore(page: Page) {
  const s = server()
  const uploads: string[] = []
  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const key = `${req.method()} ${new URL(req.url()).pathname}`
    switch (key) {
      case `GET /api/servers/${s.id}/backups`:
        return route.fulfill({ json: [backup] })
      case `GET /api/servers/${s.id}/world-copies`:
        return route.fulfill({ json: [] })
      case `GET /api/machines/${machine.id}/catalog`:
        return route.fulfill({ json: catalog })
      case `POST /api/servers/${s.id}/restore/upload`:
        uploads.push(key)
        return route.fulfill({ json: preview(s.id) })
      case `POST /api/machines/${machine.id}/restore/upload`:
        uploads.push(key)
        return route.fulfill({ json: preview() })
      default:
        return route.fallback()
    }
  })
  return uploads
}

for (const size of sizes) {
  test.describe(size.name, () => {
    test.use({ viewport: size.viewport, isMobile: size.phone, hasTouch: size.phone })

    test('a backup file chosen on the World tab or for a new server is sent to the panel', async ({ page }) => {
      const { server: s, unexpected } = await fakePanel(page, new FakeConsole())
      const uploads = await fakeRestore(page)

      await page.goto(`/servers/${s.slug}/world`)
      if (size.phone) await page.getByRole('button', { name: 'Restore a world' }).click()
      await page.getByLabel('Drop a backup file here').setInputFiles(file)
      await expect.poll(() => uploads, { message: 'the World tab sent no upload' }).toEqual([`POST /api/servers/${s.id}/restore/upload`])

      await page.goto('/servers/new')
      await page.getByRole('button', { name: 'Restore it as a new server' }).click()
      await page.getByLabel('Drop a backup file here').setInputFiles(file)
      await expect.poll(() => uploads, { message: 'the new-server page sent no upload' }).toHaveLength(2)
      expect(uploads[1]).toBe(`POST /api/machines/${machine.id}/restore/upload`)
      expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
    })
  })
}
