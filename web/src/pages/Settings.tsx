import { useEffect, useState, type FormEvent } from 'react'
import { ApiError, get, post } from '../api/client'
import type { AuditEntry, Catalog } from '../api/types'
import type { PageProps } from '../App'
import { Banner, Card, Empty, Spinner } from '../components/ui'
import { formatDateTime, formatMB } from '../lib/format'
import { navigate } from '../lib/router'
import { usePoll } from '../lib/usePoll'

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
