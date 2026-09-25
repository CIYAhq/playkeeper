import { useEffect, useState, type FormEvent } from 'react'
import { ApiError, get, post } from '../api/client'
import type { AuditEntry, Catalog, UpdateInfo, UpdateResult } from '../api/types'
import type { PageProps } from '../App'
import { Banner, Card, Empty, Spinner } from '../components/ui'
import { formatDateTime, formatMB, relativeTime } from '../lib/format'
import { navigate } from '../lib/router'
import { usePoll } from '../lib/usePoll'
import { upgradeTargets } from '../lib/versions'

type Msg = { tone: 'good' | 'bad'; text: string; hint?: string }

export function SettingsPage({ status, refresh, me, onSignedOut }: PageProps & { onSignedOut: () => void }) {
  return (
    <>
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <div className="sub">Server settings, your account and the audit log.</div>
        </div>
      </div>
      {status?.config ? <ServerSettings key={status.config.createdAt} status={status} refresh={refresh} /> : <Card title="Server settings"><Empty title="No server yet">Create a server first.</Empty></Card>}
      {status?.config && <MinecraftVersion key={`${status.config.versionId}-${status.config.paperBuild}`} status={status} refresh={refresh} />}
      <Updates status={status} refresh={refresh} version={me.version} />
      <Account username={me.user.username} onSignedOut={onSignedOut} />
      <Audit />
      <Card title="About">
        <dl className="kv">
          <dt>Playkeeper</dt>
          <dd>{me.version}</dd>
          <dt>Minecraft runtime image</dt>
          <dd>
            <code>{status?.config?.image ?? '—'}</code>
          </dd>
          <dt>Server software</dt>
          <dd>{status?.config ? `Paper ${status.config.minecraftVersion} build ${status.config.paperBuild}${status.config.jarVerifiedAt ? `, checksum verified ${formatDateTime(status.config.jarVerifiedAt)}` : ''}` : '—'}</dd>
          <dt>Minecraft EULA</dt>
          <dd>{status?.config ? `Accepted by ${status.config.eulaAcceptedBy} on ${formatDateTime(status.config.eulaAcceptedAt)}` : 'Not accepted yet'}</dd>
        </dl>
        <p className="muted small" style={{ marginTop: 10 }}>
          Playkeeper is not an official Minecraft product. Not approved by or associated with Mojang or Microsoft. Paper is downloaded from PaperMC and the Minecraft server from Mojang, on your server, after you accept the EULA.
        </p>
      </Card>
    </>
  )
}

function ServerSettings({ status, refresh }: Pick<PageProps, 'refresh'> & { status: NonNullable<PageProps['status']> }) {
  const cfg = status.config!
  const [catalog, setCatalog] = useState<Catalog>()
  const [motd, setMotd] = useState(cfg.motd)
  const [maxPlayers, setMaxPlayers] = useState(cfg.maxPlayers)
  const [memory, setMemory] = useState(cfg.memoryMB)
  const [msg, setMsg] = useState<{ tone: 'good' | 'bad'; text: string; hint?: string }>()
  useEffect(() => {
    get<Catalog>('/api/catalog').then(setCatalog).catch(() => undefined)
  }, [])

  async function save(e: FormEvent) {
    e.preventDefault()
    setMsg(undefined)
    try {
      await post('/api/server/settings', { motd, maxPlayers, memoryMB: memory })
      setMsg({ tone: 'good', text: 'Saved. Restart the server to apply the changes.' })
      await refresh()
    } catch (err) {
      setMsg({ tone: 'bad', text: (err as ApiError).message, hint: (err as ApiError).hint })
    }
  }

  return (
    <Card title="Server settings">
      <form className="form" onSubmit={save}>
        <div className="row">
          <div className="field">
            <label htmlFor="s-motd">Server name (shown in the Minecraft server list)</label>
            <input id="s-motd" type="text" maxLength={59} value={motd} onChange={(e) => setMotd(e.target.value)} />
          </div>
          <div className="field" style={{ maxWidth: 160 }}>
            <label htmlFor="s-max">Max players</label>
            <input id="s-max" type="number" min={1} max={100} value={maxPlayers} onChange={(e) => setMaxPlayers(Number(e.target.value))} />
          </div>
          <div className="field" style={{ maxWidth: 220 }}>
            <label htmlFor="s-mem">Memory budget</label>
            <select id="s-mem" value={memory} onChange={(e) => setMemory(Number(e.target.value))}>
              {(catalog?.memoryOptionsMB ?? [cfg.memoryMB]).map((mb) => (
                <option key={mb} value={mb}>
                  {formatMB(mb)}
                </option>
              ))}
            </select>
          </div>
        </div>
        <p className="muted small">
          Game port {status.gamePort}. Only players on the allowlist can join (manage it on the Players page).{' '}
          {status.offlineModeTest ? 'Offline mode is on for the automated test harness (see the red banner).' : 'Online mode is on: only genuine Minecraft accounts can join.'}
        </p>
        {msg && <Banner tone={msg.tone} title={msg.text}>{msg.hint}</Banner>}
        <div className="actions">
          <button className="btn primary" type="submit" disabled={!!status.operation}>
            Save settings
          </button>
          {status.pendingRestart && (
            <button type="button" className="btn" disabled={status.phase !== 'online' || !!status.operation} onClick={() => post('/api/server/restart').then(refresh).catch((e: ApiError) => setMsg({ tone: 'bad', text: e.message }))}>
              Restart now
            </button>
          )}
        </div>
      </form>
    </Card>
  )
}

function MinecraftVersion({ status, refresh }: Pick<PageProps, 'refresh'> & { status: NonNullable<PageProps['status']> }) {
  const cfg = status.config!
  const [catalog, setCatalog] = useState<Catalog>()
  const [target, setTarget] = useState('')
  const [accept, setAccept] = useState(false)
  const [msg, setMsg] = useState<Msg>()
  useEffect(() => {
    get<Catalog>('/api/catalog')
      .then((c) => {
        setCatalog(c)
        setTarget(upgradeTargets(cfg, c.versions).find((v) => !v.experimental)?.id ?? '')
      })
      .catch((e: ApiError) => setMsg({ tone: 'bad', text: e.message, hint: e.hint }))
  }, [cfg])
  const targets = catalog ? upgradeTargets(cfg, catalog.versions) : []
  const chosen = targets.find((v) => v.id === target)

  async function change(e: FormEvent) {
    e.preventDefault()
    setMsg(undefined)
    try {
      await post('/api/server/version', { versionId: target, acceptExperimental: !!chosen?.experimental && accept })
      await refresh()
    } catch (err) {
      setMsg({ tone: 'bad', text: (err as ApiError).message, hint: (err as ApiError).hint })
    }
  }

  return (
    <Card title="Minecraft version" hint={`Paper ${cfg.minecraftVersion} build ${cfg.paperBuild}`}>
      {catalog?.versionsError && <Banner tone="bad" title={catalog.versionsError} />}
      {!catalog && !msg && <Spinner label="Loading versions from PaperMC…" />}
      {catalog && targets.length === 0 && !catalog.versionsError && <p className="muted">This is the newest version PaperMC offers.</p>}
      {targets.length > 0 && (
        <form className="form" onSubmit={change}>
          <div className="field" style={{ maxWidth: 360 }}>
            <label htmlFor="mc-version">Update to</label>
            <select
              id="mc-version"
              value={target}
              onChange={(e) => {
                setTarget(e.target.value)
                setAccept(false)
              }}
            >
              {!target && <option value="">Choose a version</option>}
              {targets.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.label} build {v.paperBuild}
                  {v.recommended ? ' (recommended)' : v.experimental ? ' (experimental)' : ''}
                </option>
              ))}
            </select>
          </div>
          {chosen?.experimental && (
            <Banner tone="warn" title={`${chosen.label} is experimental`}>
              <label className="check">
                <input type="checkbox" checked={accept} onChange={(e) => setAccept(e.target.checked)} /> I understand that it may crash or damage my world.
              </label>
            </Banner>
          )}
          <p className="muted small">
            Playkeeper takes a backup first, then downloads the new version and checks it against the checksum PaperMC publishes. If the server does not start on it, the backup is put back and it runs {cfg.minecraftVersion} again. After an update the world cannot go back to an older version.
          </p>
          {msg && <Banner tone={msg.tone} title={msg.text}>{msg.hint}</Banner>}
          <div className="actions">
            <button className="btn primary" type="submit" disabled={!target || !!status.operation || (!!chosen?.experimental && !accept)}>
              Back up and update
            </button>
          </div>
        </form>
      )}
      {targets.length === 0 && msg && <Banner tone={msg.tone} title={msg.text}>{msg.hint}</Banner>}
    </Card>
  )
}

/** Release notes: lines starting with "- " are list items. */
function Notes({ text }: { text?: string }) {
  const lines = (text ?? '').split('\n').filter((l) => l.trim())
  const items = lines.filter((l) => l.trim().startsWith('- '))
  return (
    <>
      {lines.filter((l) => !l.trim().startsWith('- ')).map((l) => (
        <p key={l}>{l.replace(/^#+\s*/, '')}</p>
      ))}
      {items.length > 0 && (
        <ul>
          {items.map((l) => (
            <li key={l}>{l.trim().slice(2)}</li>
          ))}
        </ul>
      )}
    </>
  )
}

function lastResultText(r: UpdateResult): string {
  return r.outcome === 'updated' ? `Updated from ${r.from} to ${r.to} on ${formatDateTime(r.finishedAt)}.` : (r.error ?? `The update to ${r.to} did not complete.`)
}

function Updates({ status, refresh, version }: Pick<PageProps, 'status' | 'refresh'> & { version: string }) {
  const [info, setInfo] = useState<UpdateInfo>()
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<Msg>()
  useEffect(() => {
    get<UpdateInfo>('/api/update')
      .then(setInfo)
      .catch(() => undefined)
  }, [status?.updateAvailable, status?.updateInstalling, status?.lastOperation?.id])

  async function run(fn: () => Promise<void>) {
    setBusy(true)
    setMsg(undefined)
    try {
      await fn()
    } catch (err) {
      setMsg({ tone: 'bad', text: (err as ApiError).message, hint: (err as ApiError).hint })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="Playkeeper updates" hint={`This server runs Playkeeper ${info?.current ?? version}`}>
      <div id="updates" className="form">
        {!info ? (
          <Spinner />
        ) : !info.supported ? (
          <p className="muted">{info.reason}</p>
        ) : info.installing ? (
          <Banner tone="busy" title={`Installing Playkeeper ${info.installing}`} />
        ) : info.available ? (
          <>
            <Banner tone="info" title={`Playkeeper ${info.latest} is available`}>
              <Notes text={info.notes} />
            </Banner>
            <p className="muted small">
              Playkeeper checks the download&apos;s signature before installing it. The dashboard restarts for a moment and your Minecraft server keeps running; if the new version does not come up healthy, the previous one is put back automatically.
            </p>
          </>
        ) : (
          <p>You have the latest version{info.checkedAt ? ` (checked ${relativeTime(info.checkedAt)})` : ''}.</p>
        )}
        {info?.checkError && <Banner tone="warn" title={info.checkError} />}
        {info?.lastResult && !info.installing && <Banner tone={info.lastResult.outcome === 'updated' ? 'good' : 'bad'} title={lastResultText(info.lastResult)} />}
        {msg && <Banner tone={msg.tone} title={msg.text}>{msg.hint}</Banner>}
        {info?.supported && !info.installing && (
          <div className="actions">
            {info.available && info.latest && (
              <button type="button" className="btn primary" disabled={busy || !!status?.operation} onClick={() => run(async () => { await post('/api/update/apply', { version: info.latest }); await refresh() })}>
                Update to {info.latest}
              </button>
            )}
            <button type="button" className="btn" disabled={busy} onClick={() => run(async () => setInfo(await post<UpdateInfo>('/api/update/check')))}>
              Check for updates
            </button>
          </div>
        )}
      </div>
    </Card>
  )
}

function Account({ username, onSignedOut }: { username: string; onSignedOut: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [msg, setMsg] = useState<{ tone: 'good' | 'bad'; text: string }>()

  async function change(e: FormEvent) {
    e.preventDefault()
    setMsg(undefined)
    try {
      await post('/api/auth/password', { currentPassword: current, newPassword: next })
      setMsg({ tone: 'good', text: 'Password changed. Other sessions were signed out.' })
      setCurrent('')
      setNext('')
    } catch (err) {
      setMsg({ tone: 'bad', text: (err as ApiError).message })
    }
  }

  async function signOutEverywhere() {
    await post('/api/auth/logout-all').catch(() => undefined)
    onSignedOut()
    navigate('/login', true)
  }

  return (
    <Card title="Your account" hint={`Signed in as ${username}`}>
      <form className="form" onSubmit={change}>
        <div className="row">
          <div className="field">
            <label htmlFor="pw-current">Current password</label>
            <input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="pw-new">New password (10+ characters)</label>
            <input id="pw-new" type="password" autoComplete="new-password" minLength={10} value={next} onChange={(e) => setNext(e.target.value)} required />
          </div>
          <button className="btn" type="submit">
            Change password
          </button>
        </div>
        {msg && <Banner tone={msg.tone} title={msg.text} />}
      </form>
      <div className="actions" style={{ marginTop: 12 }}>
        <button type="button" className="btn" onClick={signOutEverywhere}>
          Sign out everywhere
        </button>
      </div>
    </Card>
  )
}

function Audit() {
  const audit = usePoll(() => get<AuditEntry[]>('/api/audit'), 15_000)
  return (
    <Card title="Audit log" hint="Sign-ins and every change to the server (newest first). Secrets are never recorded.">
      {audit.data ? (
        audit.data.length === 0 ? (
          <Empty title="Nothing recorded yet" />
        ) : (
          <div className="table-wrap" tabIndex={0} style={{ maxHeight: 420, overflowY: 'auto' }}>
            <table>
              <thead>
                <tr>
                  <th scope="col">When</th>
                  <th scope="col">Who</th>
                  <th scope="col">Action</th>
                  <th scope="col">Result</th>
                  <th scope="col">Details</th>
                </tr>
              </thead>
              <tbody>
                {audit.data.map((a) => (
                  <tr key={`${a.source}-${a.id}`}>
                    <td>{formatDateTime(a.ts)}</td>
                    <td>{a.actor}</td>
                    <td>
                      <code>{a.action}</code>
                    </td>
                    <td>{a.result}</td>
                    <td className="muted small">{[a.target, a.detail].filter(Boolean).join(' · ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : audit.error ? (
        <Empty title="Audit log unavailable">{audit.error.message}</Empty>
      ) : (
        <Spinner />
      )}
    </Card>
  )
}
