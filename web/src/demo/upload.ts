// The live demo's world uploads: what src/lib/upload.ts exports, with the
// real upload's steps but pieces that are never sent. Each piece "arrives"
// over a moment, so the progress bar moves as it would, and the make-believe
// machine hears only how much came (worlds.ts).

import type * as real from '@/lib/upload'
import { backoffMs, pieceBytes, retryable, uploadFiles as filesForReal, uploadWorld as uploadForReal, UploadSpeed, xhrPut, type FileUploadOptions, type Put, type UploadOptions } from '@/lib/upload'
import { answer } from './engine'

export { backoffMs, pieceBytes, retryable, UploadSpeed, xhrPut }
export type { FileUploadOptions, Put, PutResult, UploadOptions, UploadProgress } from '@/lib/upload'

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

/** A small file's text, which the demo keeps so the editor can open it. */
const keptText = 64 << 10

/** Pieces that "arrive" over a moment and tell the make-believe machine only how many bytes came, and a small file's text. */
function pretendPut(o: { files: File[]; piece?: number }): Put {
  const total = Math.max(1, o.files.reduce((n, f) => n + f.size, 0))
  return async (url, body, onSent, signal) => {
    const [, n = '0', from = '0'] = /\/files\/(\d+)\?offset=(\d+)/.exec(url) ?? []
    const size = o.files[Number(n)]?.size ?? 0
    const piece = Math.min(o.piece ?? pieceBytes, size - Number(from))
    const steps = Math.max(1, Math.round((uploadMs * piece) / total / tickMs))
    for (let i = 1; i <= steps; i++) {
      await wait(tickMs, signal)
      onSent(Math.round((piece * i) / steps))
    }
    const text = size <= keptText && Number(from) === 0 && piece === size ? await body.text() : undefined
    const imp = await answer('PUT', url, { received: Number(from) + piece, text })
    return { status: 200, text: JSON.stringify(imp) }
  }
}

/** Uploads a world the way the real upload does, without sending a byte. */
export function uploadWorld(o: UploadOptions) {
  return uploadForReal({ ...o, put: pretendPut(o) })
}

/** Uploads files into a server's folder the same way. */
export function uploadFiles(o: FileUploadOptions) {
  return filesForReal({ ...o, put: pretendPut(o) })
}

// Type-checks that this module stands in for every export of the real one.
void ({ backoffMs, pieceBytes, retryable, uploadFiles, uploadWorld, UploadSpeed, xhrPut } satisfies typeof real)
