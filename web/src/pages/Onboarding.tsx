import { useEffect, useState, type FormEvent } from 'react'
import { ApiError, get, post } from '../api/client'
import type { Catalog, LogsResponse, Operation, Preflight, RestorePreview } from '../api/types'
import type { PageProps } from '../App'
import { Footer } from '../App'
import { InviteFriends } from '../components/Invite'
import { RestoreReview, UploadArchive } from '../components/Restore'
import { Banner, CopyButton, Logo, Spinner } from '../components/ui'
import { formatDuration, formatMB, joinAddress } from '../lib/format'
import { startSteps, stepIndex } from '../lib/phase'
import { navigate } from '../lib/router'
import { usePoll } from '../lib/usePoll'

type Step = 'check' | 'eula' | 'server' | 'start'
const stepNames: { id: Step; label: string }[] = [
  { id: 'check', label: 'Check server' },
  { id: 'eula', label: 'Minecraft EULA' },
  { id: 'server', label: 'Your server' },
  { id: 'start', label: 'Start' },
]

export function Onboarding({ status, statusError, refresh, me }: PageProps) {
  const [step, setStep] = useState<Step>()
  const [eula, setEula] = useState(false)
  const [opId, setOpId] = useState<string>()

  useEffect(() => {
    if (step || (!status && !statusError)) return
    if (status?.operation && (status.operation.kind === 'create' || status.operation.kind === 'restore')) {
      setOpId(status.operation.id)
      setStep('start')
    } else if (status?.exists) setStep('start')
    else setStep('check')
  }, [status, statusError, step])

  const current = stepNames.findIndex((s) => s.id === step)
  return (
    <div className="auth-shell">
      <main className="auth-card wizard" aria-labelledby="wizard-title">
        <div className="brand">
          <Logo size={26} /> Playkeeper
        </div>
        <ol className="steps" aria-label="Setup steps">
          {stepNames.map((s, i) => (
            <li key={s.id} aria-current={i === current ? 'step' : undefined} className={i < current ? 'done' : undefined}>
              {i + 1}. {s.label}
            </li>
          ))}
        </ol>
        {!step && <Spinner />}
        {step === 'check' && <CheckStep onNext={() => setStep('eula')} />}
        {step === 'eula' && <EulaStep accepted={eula} setAccepted={setEula} onNext={() => setStep('server')} onBack={() => setStep('check')} />}
        {step === 'server' && (
          <ServerStep
            onStarted={(op) => {
              setOpId(op.id)
              setStep('start')
              void refresh()
            }}
            onBack={() => setStep('eula')}
          />
        )}
        {step === 'start' && <StartStep opId={opId} status={status} />}
      </main>
      <Footer version={me.version} />
    </div>
  )
}

function CheckStep({ onNext }: { onNext: () => void }) {
  const pf = usePoll(() => get<Preflight>('/api/preflight'), 60_000)
  return (
    <>
      <div>
        <h1 id="wizard-title">Check your server</h1>
        <p className="muted">Playkeeper checks that this VPS can run a Minecraft server. Nothing is changed.</p>
      </div>
      {pf.error && <Banner tone="bad" title={pf.error.message}>{pf.error.hint}</Banner>}
      {!pf.data && !pf.error && <Spinner label="Checking…" />}
      {pf.data && (
        <ul className="checklist">
          {pf.data.checks.map((c) => (
            <li key={c.id} className={c.status === 'pass' ? 'done' : c.status === 'fail' ? 'failed' : 'warn'}>
              <span className="mark" aria-hidden="true">{c.status === 'pass' ? '✓' : c.status === 'fail' ? '✕' : '!'}</span>
              <div>
                <div>
                  <strong>{c.label}</strong> <span className="sr-only">({c.status})</span>
                </div>
                <div className="detail">{c.detail}</div>
                {c.fix && c.status !== 'pass' && <div className="detail">Fix: {c.fix}</div>}
              </div>
            </li>
          ))}
        </ul>
      )}
      <div className="actions">
        <button type="button" className="btn primary" disabled={!pf.data?.ok} onClick={onNext}>
          Continue
        </button>
        <button type="button" className="btn" onClick={() => void pf.refresh()}>
          Check again
        </button>
      </div>
    </>
  )
}

function EulaStep({ accepted, setAccepted, onNext, onBack }: { accepted: boolean; setAccepted: (v: boolean) => void; onNext: () => void; onBack: () => void }) {
  return (
    <>
      <div>
        <h1 id="wizard-title">Accept the Minecraft EULA</h1>
        <p className="muted">
          The Minecraft server software is made by Mojang. After you accept their license, Playkeeper downloads Paper from PaperMC and the Minecraft server from Mojang onto this VPS. Nothing is downloaded before you accept.
        </p>
      </div>
      <label className="check">
        <input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
        <span>
          I have read and accept the{' '}
          <a href="https://www.minecraft.net/en-us/eula" target="_blank" rel="noreferrer noopener">
            Minecraft End User License Agreement
          </a>
          .
        </span>
      </label>
      <p className="muted small">Your acceptance is recorded with your username and the time.</p>
      <div className="actions">
        <button type="button" className="btn primary" disabled={!accepted} onClick={onNext}>
          Continue
        </button>
        <button type="button" className="btn" onClick={onBack}>
          Back
        </button>
      </div>
    </>
  )
}

function ServerStep({ onStarted, onBack }: { onStarted: (op: Operation) => void; onBack: () => void }) {
  const [mode, setMode] = useState<'new' | 'restore'>('new')
  const [catalog, setCatalog] = useState<Catalog>()
  const [version, setVersion] = useState('')
  const [memory, setMemory] = useState(0)
  const [motd, setMotd] = useState('')
  const [error, setError] = useState<ApiError>()
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<RestorePreview>()

  useEffect(() => {
    get<Catalog>('/api/catalog')
      .then((c) => {
        setCatalog(c)
        setVersion(c.versions.find((v) => v.recommended)?.id ?? c.versions[0]?.id ?? '')
        setMemory(c.recommendedMemoryMB)
      })
      .catch((e: ApiError) => setError(e))
  }, [])

  async function create(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      onStarted(await post<Operation>('/api/server', { acceptEula: true, versionId: version, memoryMB: memory, motd: motd.trim() }))
    } catch (err) {
      setError(err as ApiError)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div>
        <h1 id="wizard-title">Your Minecraft server</h1>
        <p className="muted">Start a new world, or restore a world from a Playkeeper backup.</p>
      </div>
      <fieldset className="option-grid" style={{ border: 0, padding: 0, margin: 0 }}>
        <legend className="sr-only">What to set up</legend>
        <label className="option">
          <input type="radio" name="mode" checked={mode === 'new'} onChange={() => setMode('new')} />
          <span>
            <span className="title">New world</span>
            <br />
            <span className="desc">Recommended settings are already chosen.</span>
          </span>
        </label>
        <label className="option">
          <input type="radio" name="mode" checked={mode === 'restore'} onChange={() => setMode('restore')} />
          <span>
            <span className="title">Restore a backup</span>
            <br />
            <span className="desc">Bring a world from another Playkeeper server.</span>
          </span>
        </label>
      </fieldset>
      {error && <Banner tone="bad" title={error.message}>{error.hint}</Banner>}
      {mode === 'new' && catalog && (
        <form className="form" onSubmit={create}>
          <fieldset className="field" style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="label">Server version</legend>
            <div className="option-grid">
              {catalog.versions.map((v) => (
                <label className="option" key={v.id}>
                  <input type="radio" name="version" checked={version === v.id} onChange={() => setVersion(v.id)} />
                  <span>
                    <span className="title">
                      {v.label} {v.recommended && <span className="badge good">Recommended</span>}
                    </span>
                    <br />
                    <span className="desc">{v.notes}</span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
          <div className="field">
            <label htmlFor="memory">Memory for Minecraft</label>
            <select id="memory" value={memory} onChange={(e) => setMemory(Number(e.target.value))}>
              {catalog.memoryOptionsMB.map((mb) => (
                <option key={mb} value={mb}>
                  {formatMB(mb)}
                  {mb === catalog.recommendedMemoryMB ? ' (suggested)' : ''}
                </option>
              ))}
            </select>
            <span className="help">This VPS has {formatMB(catalog.hostMemoryMB)} of RAM. The server gets a hard limit of the chosen amount; part of it is used by Java itself.</span>
          </div>
          <div className="field">
            <label htmlFor="motd">Server name (optional)</label>
            <input id="motd" type="text" maxLength={59} value={motd} onChange={(e) => setMotd(e.target.value)} placeholder="A Playkeeper server" />
          </div>
          <div className="actions">
            <button className="btn primary" type="submit" disabled={busy || !version || !memory}>
              {busy ? 'Creating…' : 'Create and start server'}
            </button>
            <button type="button" className="btn" onClick={onBack}>
              Back
            </button>
          </div>
        </form>
      )}
      {mode === 'new' && !catalog && !error && <Spinner />}
      {mode === 'restore' && !preview && <UploadArchive onPreview={setPreview} />}
      {mode === 'restore' && preview && <RestoreReview preview={preview} eulaAccepted onApplied={onStarted} onCancel={() => setPreview(undefined)} />}
    </>
  )
}

function StartStep({ opId, status }: { opId?: string; status: PageProps['status'] }) {
  const [trackId, setTrackId] = useState(opId)
  const liveId = status?.operation?.id
  useEffect(() => {
    if (liveId) setTrackId(liveId)
  }, [liveId])
  const op = usePoll(() => (trackId ? get<Operation>(`/api/operations/${trackId}`) : Promise.resolve(undefined)), 1500, trackId)
  const logs = usePoll(() => get<LogsResponse>('/api/server/logs?limit=6'), 2000)
  const [retryError, setRetryError] = useState<ApiError>()
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [])
  const o = op.data
  const failed = o?.status === 'failed'
  const ready = status?.phase === 'online' && status.reachable && (!o || o.status === 'succeeded')
  const phase = o?.status === 'running' ? o.phase : status?.phase ?? ''
  const active = stepIndex(phase)
  const elapsed = o ? ((o.finishedAt ? new Date(o.finishedAt).getTime() : now) - new Date(o.startedAt).getTime()) / 1000 : 0
  const address = joinAddress(window.location.hostname, status?.gamePort ?? 25565)

  async function retry() {
    setRetryError(undefined)
    try {
      const next = await post<Operation & { noop?: boolean }>('/api/server/start')
      if (next?.id) setTrackId(next.id)
    } catch (e) {
      setRetryError(e as ApiError)
    }
  }

  if (ready) {
    return (
      <>
        <div>
          <h1 id="wizard-title">Your server is ready</h1>
          <p className="muted">
            {status.config ? `Paper ${status.config.minecraftVersion} is online.` : 'The server is online.'} Players connect with this address:
          </p>
        </div>
        <div className="card">
          <div className="copy">
            <span className="join-address">{address}</span>
            <CopyButton text={address} label="Copy address" />
          </div>
          <p className="muted small" style={{ marginTop: 8 }}>
            Playkeeper confirmed the game port answers on this server (port {status.gamePort}). Whether friends can reach it over the internet depends on your VPS provider's firewall, which Playkeeper cannot check from here.
          </p>
        </div>
        <div>
          <h2>Invite friends</h2>
          <InviteFriends online />
        </div>
        <div className="actions">
          <button type="button" className="btn primary" onClick={() => navigate('/', true)}>
            Go to dashboard
          </button>
        </div>
      </>
    )
  }

  return (
    <>
      <div>
        <h1 id="wizard-title">{failed ? 'Starting failed' : 'Starting your server'}</h1>
        <p className="muted">{o ? `Elapsed: ${formatDuration(elapsed)}` : 'Waiting for the server…'}</p>
      </div>
      <ul className="checklist" aria-live="polite">
        {startSteps.map((s, i) => {
          const done = active > i || (o?.status === 'succeeded' && i < startSteps.length - 1) || (ready && i === startSteps.length - 1)
          const cls = done ? 'done' : failed && i === Math.max(0, active) ? 'failed' : i === active ? 'active' : ''
          return (
            <li key={s.label} className={cls}>
              <span className="mark" aria-hidden="true">{done ? '✓' : cls === 'failed' ? '✕' : i === active ? <span className="spinner" /> : i + 1}</span>
              <div>
                {s.label}
                {i === active && status?.phaseDetail ? <span className="detail"> — {status.phaseDetail}</span> : null}
              </div>
            </li>
          )
        })}
      </ul>
      {failed && o && (
        <Banner tone="bad" title={o.error ?? 'The operation failed.'}>
          {o.hint}
        </Banner>
      )}
      {retryError && <Banner tone="bad" title={retryError.message}>{retryError.hint}</Banner>}
      {logs.data && logs.data.lines.length > 0 && (
        <div>
          <h2 className="small muted">Latest server output</h2>
          <div className="console" style={{ height: 'auto', maxHeight: 180 }}>
            {logs.data.lines.map((l) => (
              <div className="line" key={l.seq}>
                {l.text}
              </div>
            ))}
          </div>
        </div>
      )}
      {failed && (
        <div className="actions">
          <button type="button" className="btn primary" onClick={retry}>
            Try again
          </button>
          <button type="button" className="btn" onClick={() => navigate('/', true)}>
            Go to dashboard
          </button>
        </div>
      )}
    </>
  )
}
