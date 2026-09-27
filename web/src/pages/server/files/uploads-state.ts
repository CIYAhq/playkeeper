import { useCallback, useEffect, useRef, useState } from 'react'
import { api, del, writeHeaders } from '@/api/client'
import { uploadsApi } from '@/api/files'
import type { FileUpload } from '@/api/types'
import { errorText } from '@/api/workspace'
import type { Picked } from '@/lib/files'
import { uploadFiles, UploadSpeed } from '@/lib/upload'

export type BatchState = 'waiting' | 'uploading' | 'done' | 'failed'

/** Files dropped or picked together, uploaded into one folder in one go. */
export interface UploadBatch {
  id: number
  folder: string
  picked: Picked[]
  replace: boolean
  state: BatchState
  sent: number
  total: number
  retrying: boolean
  secondsLeft?: number
  /** The upload as the machine last described it. */
  upload?: FileUpload
  error?: string
}

/** The files of a batch the machine couldn't put in place, with why. */
export function unplaced(b: UploadBatch): { index: number; name: string; error: string }[] {
  return (b.upload?.files ?? []).filter((f) => f.received === f.size && !f.placed && f.error).map((f) => ({ index: f.index, name: f.name, error: f.error ?? '' }))
}

/**
 * The Files tab's uploads, one batch at a time, each resumable after a
 * dropped connection. Leaving the tab, or reloading or closing the page,
 * stops them and deletes what arrived; files already put in place stay.
 * onPlaced hears of each folder files went into.
 */
export function useFileUploads(serverId: string, onPlaced: (folder: string) => void) {
  const [batches, setBatches] = useState<UploadBatch[]>([])
  const shown = useRef<UploadBatch[]>([])
  useEffect(() => {
    shown.current = batches
  })
  const seq = useRef(0)
  const current = useRef<{ id: number; ctl: AbortController; upload?: FileUpload }>(undefined)
  const placed = useRef(onPlaced)
  useEffect(() => {
    placed.current = onPlaced
  })
  const base = uploadsApi(serverId)

  const change = useCallback((id: number, next: Partial<UploadBatch>) => {
    setBatches((all) => all.map((b) => (b.id === id ? { ...b, ...next } : b)))
  }, [])

  /** Forgets an upload on the machine; leaving: the page is going away, so the request must outlive it. */
  const forget = useCallback(
    (up: FileUpload | undefined, leaving = false) => {
      if (!up) return
      const path = `${base}/${up.id}`
      if (leaving) void fetch(path, { method: 'DELETE', keepalive: true, headers: writeHeaders(), credentials: 'same-origin' }).catch(() => undefined)
      else void del(path).catch(() => undefined)
    },
    [base],
  )

  const run = useCallback(
    (b: UploadBatch) => {
      const ctl = new AbortController()
      const job: { id: number; ctl: AbortController; upload?: FileUpload } = { id: b.id, ctl, upload: b.upload }
      current.current = job
      const speed = new UploadSpeed()
      change(b.id, { state: 'uploading', retrying: false, error: undefined })
      uploadFiles({
        base,
        folder: b.folder,
        files: b.picked.map((p) => p.file),
        paths: b.picked.map((p) => p.path),
        replace: b.replace,
        resume: b.upload,
        signal: ctl.signal,
        onUpload: (up) => {
          job.upload = up
          change(b.id, { upload: up })
        },
        onProgress: (p) => {
          if (ctl.signal.aborted) return
          if (p.retrying) speed.reset()
          else speed.add(p.sent, Date.now())
          change(b.id, { sent: p.sent, total: p.total, retrying: p.retrying, secondsLeft: speed.secondsLeft(p.total) })
        },
      }).then(
        (up) => {
          if (ctl.signal.aborted) return
          current.current = undefined
          const failed = up.files.some((f) => !f.placed)
          change(b.id, { state: 'done', upload: up, sent: b.total, retrying: false })
          // An upload whose files are all in place isn't needed anymore; one with a file to try again is kept.
          if (!failed) forget(up)
          placed.current(b.folder)
        },
        (e: unknown) => {
          if (ctl.signal.aborted) return
          current.current = undefined
          change(b.id, { state: 'failed', error: errorText(e), retrying: false })
          placed.current(b.folder)
        },
      )
    },
    [base, change, forget],
  )

  // One batch at a time, in the order they came.
  useEffect(() => {
    if (batches.some((b) => b.state === 'uploading')) return
    const next = batches.find((b) => b.state === 'waiting')
    if (next) run(next)
  }, [batches, run])

  const add = useCallback((folder: string, picked: Picked[], replace: boolean) => {
    if (picked.length === 0) return
    const id = ++seq.current
    const total = picked.reduce((n, p) => n + p.file.size, 0)
    setBatches((all) => [...all, { id, folder, picked, replace, state: 'waiting', sent: 0, total, retrying: false }])
  }, [])

  const cancel = useCallback(
    (id: number) => {
      const job = current.current?.id === id ? current.current : undefined
      if (job) {
        job.ctl.abort()
        current.current = undefined
      }
      forget(job?.upload ?? shown.current.find((b) => b.id === id)?.upload)
      setBatches((all) => all.filter((b) => b.id !== id))
    },
    [forget],
  )

  /** Carries on with a batch that stopped, from what the machine has. */
  const retry = useCallback((id: number) => change(id, { state: 'waiting' }), [change])

  /** Tries again to put a whole file in place, after the machine couldn't: its last byte again, with nothing after it. */
  const place = useCallback(
    async (id: number, index: number) => {
      const b = shown.current.find((x) => x.id === id)
      const upload = b?.upload
      const f = upload?.files[index]
      if (!b || !upload || !f) return
      try {
        const up = await api<FileUpload>('PUT', `${base}/${upload.id}/files/${index}?offset=${f.size}`, undefined, new Blob([], { type: 'application/octet-stream' }))
        change(id, { upload: up })
        if (up.files.every((x) => x.placed)) forget(up)
        placed.current(b.folder)
      } catch (e) {
        change(id, { upload: { ...upload, files: upload.files.map((x) => (x.index === index ? { ...x, error: errorText(e) } : x)) } })
      }
    },
    [base, change, forget],
  )

  const dismiss = useCallback(
    (id: number) => {
      const b = shown.current.find((x) => x.id === id)
      if (b && b.state !== 'uploading') forget(b.upload)
      setBatches((all) => all.filter((x) => x.id !== id))
    },
    [forget],
  )

  /** Stops every upload and deletes what arrived, as leaving the tab does. */
  const stopAll = useCallback(
    (leaving = false) => {
      const job = current.current
      job?.ctl.abort()
      current.current = undefined
      const all = shown.current
      for (const b of all) forget(b.id === job?.id ? (job.upload ?? b.upload) : b.upload, leaving)
      setBatches([])
    },
    [forget],
  )

  const stop = useRef(stopAll)
  useEffect(() => {
    stop.current = stopAll
  })
  useEffect(() => {
    const leave = () => stop.current(true)
    window.addEventListener('pagehide', leave)
    return () => {
      window.removeEventListener('pagehide', leave)
      stop.current()
    }
  }, [])

  const busy = batches.some((b) => b.state === 'uploading' || b.state === 'waiting')
  return { batches, add, cancel, retry, place, dismiss, stopAll, busy }
}

export type FileUploads = ReturnType<typeof useFileUploads>
