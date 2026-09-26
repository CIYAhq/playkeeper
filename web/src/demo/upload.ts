// The live demo's world uploads: what src/lib/upload.ts exports, with the
// real upload's steps but pieces that are never sent. Each piece "arrives"
// over a moment, so the progress bar moves as it would, and the make-believe
// machine hears only how much came (worlds.ts).

import type * as real from '@/lib/upload'
import { backoffMs, pieceBytes, retryable, uploadWorld as uploadForReal, UploadSpeed, xhrPut, type Put, type UploadOptions } from '@/lib/upload'
import { put } from './client'

export { backoffMs, pieceBytes, retryable, UploadSpeed, xhrPut }
export type { Put, PutResult, UploadOptions, UploadProgress } from '@/lib/upload'

/** However big the world, its upload takes about this long. */
const uploadMs = 4000
const tickMs = 100

function wait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const id = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(id)
        reject(signal.reason)
      },
      { once: true },
    )
  })
}

/** Uploads files the way the real upload does, without sending a byte. */
export function uploadWorld(o: UploadOptions) {
  const total = Math.max(1, o.files.reduce((n, f) => n + f.size, 0))
  const pretend: Put = async (url, _body, onSent, signal) => {
    const [, n = '0', from = '0'] = /\/files\/(\d+)\?offset=(\d+)/.exec(url) ?? []
    const size = o.files[Number(n)]?.size ?? 0
    const piece = Math.min(o.piece ?? pieceBytes, size - Number(from))
    const steps = Math.max(1, Math.round((uploadMs * piece) / total / tickMs))
    for (let i = 1; i <= steps; i++) {
      await wait(tickMs, signal)
      onSent(Math.round((piece * i) / steps))
    }
    const imp = await put(url, { received: Number(from) + piece })
    return { status: 200, text: JSON.stringify(imp) }
  }
  return uploadForReal({ ...o, put: pretend })
}

// Type-checks that this module stands in for every export of the real one.
void ({ backoffMs, pieceBytes, retryable, uploadWorld, UploadSpeed, xhrPut } satisfies typeof real)
