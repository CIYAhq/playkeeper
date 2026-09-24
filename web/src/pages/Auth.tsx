import { useState, type FormEvent } from 'react'
import { ApiError, post } from '../api/client'
import type { Me } from '../api/types'
import { Footer } from '../App'
import { Banner, Logo } from '../components/ui'

function codeFromHash(): string {
  const m = /[#&]code=([a-z0-9-]+)/i.exec(window.location.hash)
  if (m) window.history.replaceState(null, '', window.location.pathname)
  return m?.[1] ?? ''
}

export function Setup({ onDone }: { onDone: (m: Me) => void }) {
  const [code, setCode] = useState(codeFromHash)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    if (password !== confirm) {
      setError('The two passwords do not match.')
      return
    }
    setBusy(true)
    try {
      onDone(await post<Me>('/api/setup', { token: code.trim(), username: username.trim(), password }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth-shell">
      <main className="auth-card" aria-labelledby="setup-title">
        <div className="brand">
          <Logo size={26} /> Playkeeper
        </div>
        <div>
          <h1 id="setup-title">Create your admin account</h1>
          <p className="muted">This is the only account that can manage this server. You need the one-time setup code the installer printed.</p>
        </div>
        {error && <Banner tone="bad" title={error} />}
        <form className="form" onSubmit={submit}>
          <div className="field">
            <label htmlFor="code">Setup code</label>
            <input id="code" type="text" autoComplete="one-time-code" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} required placeholder="xxxxxx-xxxxxx-xxxxxx-xxxxxx" />
            <span className="help">
              Lost it? On the server run <code>sudo playkeeper setup-code</code>.
            </span>
          </div>
          <div className="field">
            <label htmlFor="username">Username</label>
            <input id="username" type="text" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required minLength={3} maxLength={32} />
          </div>
          <div className="field">
            <label htmlFor="password">Password</label>
            <input id="password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={10} maxLength={256} aria-describedby="pw-help" />
            <span className="help" id="pw-help">
              At least 10 characters.
            </span>
          </div>
          <div className="field">
            <label htmlFor="confirm">Repeat password</label>
            <input id="confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required minLength={10} />
          </div>
          <button className="btn primary" type="submit" disabled={busy}>
            {busy ? 'Creating account…' : 'Create account'}
          </button>
        </form>
      </main>
      <Footer />
    </div>
  )
}

export function Login({ onDone }: { onDone: (m: Me) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    setBusy(true)
    try {
      onDone(await post<Me>('/api/auth/login', { username: username.trim(), password }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth-shell">
      <main className="auth-card" aria-labelledby="login-title">
        <div className="brand">
          <Logo size={26} /> Playkeeper
        </div>
        <h1 id="login-title">Sign in</h1>
        {error && <Banner tone="bad" title={error} />}
        <form className="form" onSubmit={submit}>
          <div className="field">
            <label htmlFor="username">Username</label>
            <input id="username" type="text" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
          </div>
          <div className="field">
            <label htmlFor="password">Password</label>
            <input id="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          <button className="btn primary" type="submit" disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
          <p className="muted small">
            Forgot your password? On the server run <code>sudo playkeeper reset-password {'<username>'}</code>.
          </p>
        </form>
      </main>
      <Footer />
    </div>
  )
}
