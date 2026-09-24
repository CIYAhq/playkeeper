import { useState } from 'react'
import { ApiError, del, get, post } from '../api/client'
import type { Backup, Operation, RestorePreview } from '../api/types'
import type { PageProps } from '../App'
import { RestoreReview, UploadArchive } from '../components/Restore'
import { Banner, Card, CopyButton, Empty, Modal, Spinner } from '../components/ui'
import { formatBytes, formatDateTime, formatMs, relativeTime } from '../lib/format'
import { usePoll } from '../lib/usePoll'
import { opText, phaseText } from './Overview'

export function WorldPage({ status, refresh }: PageProps) {
  const backups = usePoll(() => get<Backup[]>('/api/backups'), 5000)
  const [confirmBackup, setConfirmBackup] = useState(false)
  const [note, setNote] = useState('')
  const [error, setError] = useState<ApiError>()
  const [preview, setPreview] = useState<RestorePreview>()
  const [started, setStarted] = useState<Operation>()
  const [toDelete, setToDelete] = useState<Backup>()
  const busy = status?.operation !== undefined
  const running = status?.phase === 'online'
  const lastDowntime = backups.data?.find((b) => b.kind === 'manual' && b.downtimeMs > 0)?.downtimeMs

  async function makeBackup() {
    setConfirmBackup(false)
    setError(undefined)
    try {
      setStarted(await post<Operation>('/api/backups', { note: note.trim() }))
      setNote('')
      await refresh()
    } catch (e) {
      setError(e as ApiError)
    }
  }

  async function verify(b: Backup) {
    setError(undefined)
    try {
      await post(`/api/backups/${b.id}/verify`)
      await backups.refresh()
    } catch (e) {
      setError(e as ApiError)
    }
  }

  async function stage(b: Backup) {
    setError(undefined)
    try {
      setPreview(await post<RestorePreview>(`/api/backups/${b.id}/restore`))
      window.setTimeout(() => document.getElementById('restore-review')?.scrollIntoView({ block: 'start' }), 50)
    } catch (e) {
      setError(e as ApiError)
    }
  }

  async function remove() {
    if (!toDelete) return
    try {
      await del(`/api/backups/${toDelete.id}`)
      setToDelete(undefined)
      await backups.refresh()
    } catch (e) {
      setError(e as ApiError)
      setToDelete(undefined)
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>World</h1>
          <div className="sub">Backups, downloads and restores for “{status?.config?.levelName ?? 'world'}”.</div>
        </div>
      </div>
      <Banner tone="warn" title="Backups listed here are stored on this server only">
        They are not disaster recovery: if this VPS or its disk is lost, they are lost with it. Download each backup you want to keep and store it somewhere else (your computer or another provider). Playkeeper cannot see where downloaded copies end up.
      </Banner>
      {error && (
        <Banner tone="bad" title={error.message}>
          {error.hint}
        </Banner>
      )}
      {status?.operation && (status.operation.kind === 'backup' || status.operation.kind === 'restore') && (
        <Banner tone="busy" title={`${opText(status.operation.kind)}: ${phaseText(status.operation.phase)}`}>
          Started {relativeTime(status.operation.startedAt)} by {status.operation.actor}.
        </Banner>
      )}
      {started && !busy && status?.lastOperation?.id === started.id && status.lastOperation.status === 'succeeded' && (
        <Banner tone="good" title={`${started.kind === 'backup' ? 'Backup' : 'Restore'} finished`}>
          {typeof status.lastOperation.detail?.downtimeMs === 'number' ? `The server was offline for ${formatMs(status.lastOperation.detail.downtimeMs as number)}.` : null}
        </Banner>
      )}
      <Card title="Make a backup" hint="Consistent copy of the world and its settings">
        <p className="muted">
          To make a consistent copy, Playkeeper saves the world and <strong>stops the server while the archive is written</strong>, then starts it again. Players are disconnected during that time.
          {lastDowntime ? ` The last backup took the server offline for ${formatMs(lastDowntime)}.` : ''} The archive is then read back and checked file by file.
        </p>
        <div className="row">
          <div className="field">
            <label htmlFor="note">Note (optional)</label>
            <input id="note" type="text" maxLength={200} value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. before the big build" />
          </div>
          <button type="button" className="btn primary" disabled={busy || !status?.exists} onClick={() => setConfirmBackup(true)}>
            Back up now
          </button>
        </div>
      </Card>
      <Modal open={confirmBackup} onClose={() => setConfirmBackup(false)} title="Back up now?">
        <p>{running ? 'The server will stop while the archive is written, then start again automatically. Online players will be disconnected.' : 'The server is not running, so no downtime is needed.'}</p>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => setConfirmBackup(false)}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={makeBackup}>
            {running ? 'Stop, back up and restart' : 'Back up'}
          </button>
        </div>
      </Modal>
      <Card title="Backups on this server" hint="Newest first">
        {backups.data ? (
          backups.data.length === 0 ? (
            <Empty title="No backups yet">Make your first backup above, then download it.</Empty>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th scope="col">Made</th>
                    <th scope="col">Type</th>
                    <th scope="col">Integrity</th>
                    <th scope="col" className="num">Size</th>
                    <th scope="col" className="num">Downtime</th>
                    <th scope="col">Stored</th>
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {backups.data.map((b) => (
                    <tr key={b.id}>
                      <td>
                        {formatDateTime(b.createdAt)}
                        <div className="muted small">
                          Minecraft {b.minecraftVersion} · {b.fileCount} files{b.note ? ` · ${b.note}` : ''}
                        </div>
                      </td>
                      <td>{b.kind === 'rollback' ? <span className="badge">Rollback</span> : <span className="badge">Manual</span>}</td>
                      <td>
                        {b.verified ? <span className="badge good">Verified</span> : b.verified === false ? <span className="badge bad">Failed check</span> : <span className="badge">Not checked</span>}
                        <div className="muted small" title={b.sha256}>
                          SHA-256 <code>{b.sha256.slice(0, 12)}…</code> <CopyButton text={b.sha256} label="Copy" />
                        </div>
                        {b.verifyError && <div className="small" style={{ color: 'var(--bad)' }}>{b.verifyError}</div>}
                      </td>
                      <td className="num">{formatBytes(b.sizeBytes)}</td>
                      <td className="num">{b.downtimeMs ? formatMs(b.downtimeMs) : '—'}</td>
                      <td>
                        <span className="badge warn">This server only</span>
                        <div className="muted small">{b.downloadedAt ? `Downloaded ${relativeTime(b.downloadedAt)}` : 'Not downloaded'}</div>
                      </td>
                      <td>
                        <div className="actions">
                          <a className="btn small" href={`/api/backups/${b.id}/download`} download={b.fileName} onClick={() => window.setTimeout(() => void backups.refresh(), 3000)}>
                            Download
                          </a>
                          <button type="button" className="btn small" onClick={() => verify(b)} disabled={busy}>
                            Check again
                          </button>
                          <button type="button" className="btn small" onClick={() => stage(b)} disabled={busy}>
                            Restore…
                          </button>
                          <button type="button" className="btn small ghost" onClick={() => setToDelete(b)} disabled={busy} aria-label={`Delete backup from ${formatDateTime(b.createdAt)}`}>
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        ) : backups.error ? (
          <Empty title="Backups unavailable">{backups.error.message}</Empty>
        ) : (
          <Spinner />
        )}
      </Card>
      <Modal open={!!toDelete} onClose={() => setToDelete(undefined)} title="Delete this backup?">
        <p>The archive from {formatDateTime(toDelete?.createdAt)} will be removed from this server. Copies you downloaded are not affected.</p>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => setToDelete(undefined)}>
            Cancel
          </button>
          <button type="button" className="btn danger" onClick={remove}>
            Delete backup
          </button>
        </div>
      </Modal>
      <Card title="Restore a world" hint="From a downloaded Playkeeper backup or one of the backups above">
        <div id="restore-review">
          {preview ? (
            <RestoreReview
              preview={preview}
              onApplied={async (op) => {
                setStarted(op)
                setPreview(undefined)
                await refresh()
              }}
              onCancel={() => setPreview(undefined)}
            />
          ) : (
            <UploadArchive onPreview={setPreview} />
          )}
        </div>
      </Card>
    </>
  )
}
