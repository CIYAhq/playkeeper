import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { WorldImport } from '@/api/types'
import { backoffMs, retryable, uploadWorld, UploadSpeed, type Put } from './upload'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
  post: vi.fn(),
}))

const base = '/api/machines/m2345abcde/world-imports'
const at = (offset: number, n = 0) => `${base}/imp2345abc/files/${n}?offset=${offset}`
const file = (name: string, size: number) => new File([new Uint8Array(size)], name)
const noWait = async () => {}

/** Keeps what arrives, like the agent: each piece must start where the last one stopped. */
class Machine {
  files: { name: string; size: number; received: number }[] = []
  puts: string[] = []
  /** For the next pieces: how many bytes arrive before the connection drops (undefined: all). */
  drops: (number | undefined)[] = []

  view(): WorldImport {
    return { id: 'imp2345abc', createdAt: '2026-09-25T10:00:00Z', limitBytes: 1 << 30, files: this.files.map((f, index) => ({ index, ...f })) }
  }

  put: Put = async (url, body) => {
    this.puts.push(url)
    const m = /\/files\/(\d+)\?offset=(\d+)$/.exec(url)
    const f = this.files[Number(m?.[1])]
    if (!f) return { status: 404, text: JSON.stringify({ error: 'File not found.', code: 'not_found' }) }
    if (Number(m?.[2]) !== f.received) return { status: 409, text: JSON.stringify({ error: `The upload carries on from byte ${f.received}.`, code: 'conflict' }) }
    const drop = this.drops.shift()
    f.received += drop ?? body.size
    return drop === undefined ? { status: 200, text: JSON.stringify(this.view()) } : { status: 0, text: '' }
  }
}

let machine: Machine

beforeEach(() => {
  machine = new Machine()
  vi.mocked(client.post).mockImplementation((async (path: string, body?: unknown) => {
    if (path !== base) {
      const b = body as { name: string; size: number }
      machine.files.push({ name: b.name, size: b.size, received: 0 })
    }
    return machine.view()
  }) as typeof client.post)
  vi.mocked(client.get).mockImplementation((async () => machine.view()) as typeof client.get)
})

describe('uploadWorld', () => {
  it('sends a file in pieces and reports each byte that arrives', async () => {
    const sent: number[] = []
    const imp = await uploadWorld({ base, files: [file('Survival-2024.zip', 25)], signal: new AbortController().signal, put: machine.put, wait: noWait, piece: 10, onProgress: (p) => sent.push(p.sent) })
    expect(machine.puts).toEqual([at(0), at(10), at(20)])
    expect(imp.files[0]?.received).toBe(25)
    expect(sent.at(-1)).toBe(25)
  })

  it('carries on from where the machine stopped after the connection drops', async () => {
    machine.drops = [undefined, 4]
    const waits: number[] = []
    const retrying: boolean[] = []
    await uploadWorld({
      base,
      files: [file('Survival-2024.zip', 25)],
      signal: new AbortController().signal,
      put: machine.put,
      wait: async (ms) => void waits.push(ms),
      piece: 10,
      onProgress: (p) => retrying.push(p.retrying),
    })
    expect(machine.puts).toEqual([at(0), at(10), at(14), at(24)])
    expect(waits).toEqual([1000])
    expect(retrying).toContain(true)
    expect(retrying.at(-1)).toBe(false)
  })

  it('asks where the upload stands after a 409 without waiting', async () => {
    machine.files = [{ name: 'world.zip', size: 25, received: 10 }]
    const resume = { ...machine.view(), files: [{ index: 0, name: 'world.zip', size: 25, received: 0 }] }
    const wait = vi.fn(noWait)
    await uploadWorld({ base, files: [file('world.zip', 25)], signal: new AbortController().signal, resume, put: machine.put, wait, piece: 10 })
    expect(machine.puts).toEqual([at(0), at(10), at(20)])
    expect(wait).not.toHaveBeenCalled()
    expect(client.post).not.toHaveBeenCalledWith(base, {})
  })

  it('stops at a refusal that waiting can’t fix', async () => {
    const put: Put = async () => ({ status: 507, text: JSON.stringify({ error: 'The disk filled up during the upload.', code: 'insufficient_space' }) })
    await expect(uploadWorld({ base, files: [file('world.zip', 25)], signal: new AbortController().signal, put, wait: noWait })).rejects.toMatchObject({ status: 507, message: 'The disk filled up during the upload.' })
  })

  it('gives up after twelve tries in a row without progress', async () => {
    const put = vi.fn<Put>(async () => ({ status: 0, text: '' }))
    const wait = vi.fn(noWait)
    await expect(uploadWorld({ base, files: [file('world.zip', 25)], signal: new AbortController().signal, put, wait })).rejects.toMatchObject({ status: 0, code: 'network' })
    expect(wait).toHaveBeenCalledTimes(12)
    expect(put).toHaveBeenCalledTimes(13)
  })

  it('stops when cancelled', async () => {
    const ctl = new AbortController()
    const put: Put = async (_url, _body, _sent, signal) => {
      ctl.abort()
      signal.throwIfAborted()
      return { status: 200, text: '' }
    }
    await expect(uploadWorld({ base, files: [file('world.zip', 25)], signal: ctl.signal, put, wait: noWait })).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('announces every file and counts their bytes together', async () => {
    const sent: number[] = []
    await uploadWorld({ base, files: [file('world.zip', 12), file('world_nether.zip', 5)], signal: new AbortController().signal, put: machine.put, wait: noWait, piece: 10, onProgress: (p) => sent.push(p.sent) })
    expect(machine.files.map((f) => f.name)).toEqual(['world.zip', 'world_nether.zip'])
    expect(machine.puts).toEqual([at(0), at(10), at(0, 1)])
    expect(sent.at(-1)).toBe(17)
  })
})

describe('upload helpers', () => {
  it('retries only what can work later', () => {
    expect([0, 400, 409, 429, 502, 503].map(retryable)).toEqual([true, true, true, true, true, true])
    expect([401, 403, 404, 413, 422, 507].map(retryable)).toEqual([false, false, false, false, false, false])
  })

  it('waits longer after each try, up to 30 seconds', () => {
    expect([1, 2, 3, 5, 6, 12].map(backoffMs)).toEqual([1000, 2000, 4000, 16000, 30000, 30000])
  })

  it('estimates the time left from the last seconds', () => {
    const s = new UploadSpeed()
    s.add(0, 0)
    s.add(10, 1000)
    expect(s.secondsLeft(100)).toBeUndefined()
    s.add(20, 2000)
    expect(s.secondsLeft(100)).toBe(8)
    s.add(5, 3000)
    expect(s.secondsLeft(100)).toBeUndefined()
  })
})
