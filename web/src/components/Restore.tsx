import { useState, type FormEvent } from 'react'
import { api, ApiError, del, post } from '../api/client'
import type { Operation, RestorePreview } from '../api/types'
import { formatBytes, formatDateTime, formatMB } from '../lib/format'
import { Banner } from './ui'

/** Upload an archive and get a validated preview. Nothing changes yet. */
export function UploadArchive({ onPreview }: { onPreview: (p: RestorePreview) => void }) {
  const [file, setFile] = useState<File>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<ApiError>()

  async function upload(e: FormEvent) {
    e.preventDefault()
    if (!file) return
    setBusy(true)
    setError(undefined)
    try {
      onPreview(await api<RestorePreview>('POST', '/api/restore/upload', undefined, file))
    } catch (err) {
      setError(err as ApiError)
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="stack" onSubmit={upload}>
      <div className="field">
        <label htmlFor="archive">Playkeeper backup file (.tar.gz)</label>
        <input id="archive" type="file" accept=".tar.gz,.tgz,application/gzip" onChange={(e) => setFile(e.target.files?.[0])} />
        <span className="help">The file is checked completely (every file's SHA-256) before anything on this server changes.</span>
      </div>
      {error && (
        <Banner tone="bad" title={error.message}>
          {error.hint}
        </Banner>
      )}
      <div className="actions">
        <button className="btn" type="submit" disabled={!file || busy}>
          {busy ? 'Uploading and checking…' : 'Upload and check'}
        </button>
      </div>
    </form>
  )
}

/** Shows exactly what a restore will do and asks for explicit confirmation. */
export function RestoreReview({ preview, eulaAccepted, onApplied, onCancel }: { preview: RestorePreview; eulaAccepted?: boolean; onApplied: (op: Operation) => void; onCancel: () => void }) {
  const [typed, setTyped] = useState('')
  const [eula, setEula] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<ApiError>()
  const m = preview.manifest
  const needsTyping = preview.currentWorld.exists
  const eulaOk = !preview.needsEula || eulaAccepted || eula
  const canApply = preview.compatible && eulaOk && (!needsTyping || typed.trim() === preview.confirmPhrase)

  async function apply() {
    setBusy(true)
    setError(undefined)
    try {
      onApplied(await post<Operation>(`/api/restore/${preview.id}/apply`, { confirm: needsTyping ? typed.trim() : preview.confirmPhrase, acceptEula: preview.needsEula && (eulaAccepted || eula) }))
    } catch (err) {
      setError(err as ApiError)
    } finally {
      setBusy(false)
    }
  }

  async function cancel() {
    await del(`/api/restore/${preview.id}`).catch(() => undefined)
    onCancel()
  }

  return (
    <div className="stack" aria-live="polite">
      <Banner tone={preview.compatible ? 'good' : 'bad'} title={preview.compatible ? 'The backup file is intact and can be restored' : 'This backup cannot be restored here'}>
        Checked {m?.fileCount ?? 0} files against their SHA-256 hashes. Archive SHA-256: <code>{preview.sha256}</code>
      </Banner>
      {m && (
        <dl className="kv">
          <dt>World</dt>
          <dd>{m.levelName}</dd>
          <dt>Made</dt>
          <dd>
            {formatDateTime(m.createdAt)} ({preview.source})
          </dd>
          <dt>Server software</dt>
          <dd>
            Minecraft {m.minecraftVersion}, Paper build {m.paperBuild}
          </dd>
          <dt>Size</dt>
          <dd>
            {formatBytes(preview.sizeBytes)} archive, {formatBytes(m.totalBytes)} of files
          </dd>
          <dt>Memory budget</dt>
          <dd>{formatMB(preview.memoryMB)}</dd>
        </dl>
      )}
      {preview.problems.map((p) => (
        <Banner key={p} tone="bad" title={p} />
      ))}
      {preview.warnings.map((w) => (
        <Banner key={w} tone="warn" title={w} />
      ))}
      <div>
        <h3>What will happen</h3>
        <ol className="plain">
          {preview.steps.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ol>
      </div>
      <div>
        <h3>Not included in the backup</h3>
        <ul className="plain muted small">
          {preview.notRestored.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ul>
      </div>
      {preview.needsEula && !eulaAccepted && (
        <label className="check">
          <input type="checkbox" checked={eula} onChange={(e) => setEula(e.target.checked)} />
          <span>
            I have read and accept the{' '}
            <a href="https://www.minecraft.net/en-us/eula" target="_blank" rel="noreferrer noopener">
              Minecraft End User License Agreement
            </a>
            .
          </span>
        </label>
      )}
      {needsTyping && (
        <div className="field">
          <label htmlFor="confirm-restore">
            This replaces the current world “{preview.currentWorld.levelName}” ({formatBytes(preview.currentWorld.sizeBytes)}). A rollback archive of it is saved first. Type <strong>{preview.confirmPhrase}</strong> to confirm.
          </label>
          <input id="confirm-restore" type="text" autoComplete="off" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} />
        </div>
      )}
      {error && (
        <Banner tone="bad" title={error.message}>
          {error.hint}
        </Banner>
      )}
      <div className="actions">
        <button type="button" className={needsTyping ? 'btn danger' : 'btn primary'} disabled={!canApply || busy} onClick={apply}>
          {busy ? 'Starting restore…' : needsTyping ? 'Replace world and restore' : 'Restore this world'}
        </button>
        <button type="button" className="btn" onClick={cancel} disabled={busy}>
          Cancel
        </button>
      </div>
    </div>
  )
}
