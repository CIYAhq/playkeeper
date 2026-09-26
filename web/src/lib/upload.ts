import { ApiError, get, post, responseError, writeHeaders } from '@/api/client'
import type { WorldImport } from '@/api/types'
import { t } from '@/i18n'

// Resumable world uploads. Each file is announced, then sent in pieces from
// the byte the machine says it has. After a dropped connection the upload
// asks where it stands and carries on from there.

/** One request's share of a file: below the 100 MB common proxies accept. */
export const pieceBytes = 64 << 20

/** How long a piece may send nothing before it counts as dropped. */
const stallMs = 90_000

/** Failed tries in a row, without progress, before the upload gives up. */
const maxTries = 12

export interface PutResult {
  status: number
  text: string
}

/** Sends one piece. A failed connection resolves with status 0; an abort rejects. */
export type Put = (url: string, body: Blob, onSent: (bytes: number) => void, signal: AbortSignal) => Promise<PutResult>

export interface UploadProgress {
  /** Bytes the machine has, plus the part of the current piece on its way. */
  sent: number
  total: number
  /** The connection dropped; the upload waits, then carries on. */
  retrying: boolean
}

export interface UploadOptions {
  /** The machine's uploads, /api/machines/{mid}/world-imports. */
  base: string
  files: File[]
  signal: AbortSignal
  /** An upload an earlier try opened, to carry on with. */
  resume?: WorldImport
  /** The upload as the machine last described it, after each answer. */
  onImport?: (imp: WorldImport) => void
  onProgress?: (p: UploadProgress) => void
  put?: Put
  wait?: (ms: number, signal: AbortSignal) => Promise<void>
  piece?: number
}

/**
 * Uploads files into a world import and returns it once the machine has every byte.
 * Carrying on with an upload asks the machine which files it has first and announces only the rest.
 */
export async function uploadWorld(o: UploadOptions): Promise<WorldImport> {
  const put = o.put ?? xhrPut
  const wait = o.wait ?? sleep
  const piece = o.piece ?? pieceBytes
  const total = o.files.reduce((n, f) => n + f.size, 0)
  const seen = (imp: WorldImport) => {
    o.onImport?.(imp)
    return imp
  }
  let imp = seen(o.resume ? await get<WorldImport>(`${o.base}/${o.resume.id}`) : await post<WorldImport>(o.base, {}))
  const path = `${o.base}/${imp.id}`
  if (imp.files.length > o.files.length || imp.files.some((f, n) => f.name !== o.files[n]?.name || f.size !== o.files[n]?.size)) {
    throw new ApiError(409, { error: t('import.otherFiles'), code: 'conflict' })
  }
  for (const f of o.files.slice(imp.files.length)) {
    o.signal.throwIfAborted()
    imp = seen(await post<WorldImport>(`${path}/files`, { name: f.name, size: f.size }))
  }

  let before = 0
  for (const [n, f] of o.files.entries()) {
    let at = imp.files[n]?.received ?? 0
    let tries = 0
    const report = (sent: number, retrying = false) => o.onProgress?.({ sent: before + sent, total, retrying })
    report(at)
    while (at < f.size) {
      o.signal.throwIfAborted()
      const from = at
      const res = await put(`${path}/files/${n}?offset=${from}`, f.slice(from, Math.min(f.size, from + piece)), (bytes) => report(from + bytes), o.signal)
      let err: ApiError
      if (res.status >= 200 && res.status < 300) {
        imp = seen(JSON.parse(res.text) as WorldImport)
        at = imp.files[n]?.received ?? 0
        report(at)
        if (at > from) {
          tries = 0
          continue
        }
        err = responseError(0, '')
      } else {
        err = responseError(res.status, res.text)
        if (!retryable(err.status)) throw err
      }
      tries++
      if (tries > maxTries) throw err
      // A first 409 usually just says which byte to send from.
      if (err.status !== 409 || tries > 1) {
        report(at, true)
        await wait(backoffMs(tries), o.signal)
      }
      try {
        imp = seen(await get<WorldImport>(path))
        at = imp.files[n]?.received ?? at
        if (at > from) tries = 0
      } catch (e) {
        if (!(e instanceof ApiError) || !retryable(e.status)) throw e
      }
      report(at)
    }
    before += f.size
  }
  return imp
}

/** Can a failed request work later? Refusals such as a full disk or a gone upload can't. */
export function retryable(status: number): boolean {
  return status === 0 || status === 400 || status === 408 || status === 409 || status === 429 || (status >= 500 && status !== 501 && status !== 507)
}

/** 1, 2, 4, 8, 16, then 30 seconds between tries. */
export function backoffMs(tries: number): number {
  return Math.min(30, 2 ** Math.max(0, tries - 1)) * 1000
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason)
      return
    }
    const stop = () => {
      clearTimeout(id)
      reject(signal.reason)
    }
    const id = setTimeout(() => {
      signal.removeEventListener('abort', stop)
      resolve()
    }, ms)
    signal.addEventListener('abort', stop, { once: true })
  })
}

/** Sends a piece with XMLHttpRequest, which reports how much went out. */
export function xhrPut(url: string, body: Blob, onSent: (bytes: number) => void, signal: AbortSignal): Promise<PutResult> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason)
      return
    }
    const xhr = new XMLHttpRequest()
    let stalled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const idle = () => {
      clearTimeout(timer)
      timer = setTimeout(() => {
        stalled = true
        xhr.abort()
      }, stallMs)
    }
    const stop = () => xhr.abort()
    const finish = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', stop)
    }
    xhr.open('PUT', url)
    for (const [k, v] of Object.entries(writeHeaders())) xhr.setRequestHeader(k, v)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.upload.onprogress = (e) => {
      idle()
      onSent(e.loaded)
    }
    xhr.onload = () => {
      finish()
      resolve({ status: xhr.status, text: xhr.responseText })
    }
    xhr.onerror = () => {
      finish()
      resolve({ status: 0, text: '' })
    }
    xhr.onabort = () => {
      finish()
      if (stalled) resolve({ status: 0, text: '' })
      else reject(signal.reason)
    }
    signal.addEventListener('abort', stop, { once: true })
    idle()
    xhr.send(body)
  })
}

/** How fast an upload went over the last 20 seconds, for its time left. */
export class UploadSpeed {
  private samples: { at: number; bytes: number }[] = []

  add(bytes: number, at: number) {
    const last = this.samples[this.samples.length - 1]
    if (last && bytes < last.bytes) this.samples = []
    this.samples.push({ at, bytes })
    while (this.samples.length > 2 && at - (this.samples[0]?.at ?? at) > 20_000) this.samples.shift()
  }

  reset() {
    this.samples = []
  }

  /** Seconds until total bytes, once two seconds of samples show a speed. */
  secondsLeft(total: number): number | undefined {
    const first = this.samples[0]
    const last = this.samples[this.samples.length - 1]
    if (!first || !last || last.at - first.at < 2000) return undefined
    const perSecond = (last.bytes - first.bytes) / ((last.at - first.at) / 1000)
    return perSecond > 0 ? Math.max(0, total - last.bytes) / perSecond : undefined
  }
}
