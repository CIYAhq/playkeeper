import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { ApiError, get, onUnauthorized, post, setCsrfToken } from './api/client'
import type { Me, ServerStatus } from './api/types'
import { Banner, Icon, Logo, Spinner, StatusPill } from './components/ui'
import { navigate, useRoute, type Route } from './lib/router'
import { usePoll } from './lib/usePoll'
import { Login, Setup } from './pages/Auth'
import { ConsolePage } from './pages/Console'
import { Onboarding } from './pages/Onboarding'
import { Overview } from './pages/Overview'
import { PlayersPage } from './pages/Players'
import { SettingsPage } from './pages/Settings'
import { WorldPage } from './pages/World'

type AuthState = 'loading' | 'setup' | 'login' | 'ready' | 'offline'

export interface PageProps {
  status: ServerStatus | undefined
  statusError: ApiError | undefined
  refresh: () => Promise<void>
  me: Me
}

export function App() {
  const route = useRoute()
  const [state, setState] = useState<AuthState>('loading')
  const [me, setMe] = useState<Me>()

  const signedIn = useCallback((m: Me) => {
    setCsrfToken(m.csrfToken)
    setMe(m)
    setState('ready')
  }, [])

  useEffect(() => {
    let cancelled = false
    async function boot() {
      try {
        const s = await get<{ needsSetup: boolean }>('/api/setup/status')
        if (cancelled) return
        if (s.needsSetup) {
          setState('setup')
          if (window.location.pathname !== '/setup') navigate('/setup', true)
          return
        }
        const m = await get<Me>('/api/auth/me')
        if (!cancelled) signedIn(m)
      } catch (e) {
        if (cancelled) return
        if (e instanceof ApiError && e.status === 401) {
          setState('login')
          navigate('/login', true)
        } else setState('offline')
      }
    }
    void boot()
    const off = onUnauthorized(() => {
      setState('login')
      navigate('/login', true)
    })
    return () => {
      cancelled = true
      off()
    }
  }, [signedIn])

  if (state === 'loading') {
    return (
      <div className="auth-shell">
        <Spinner label="Loading Playkeeper…" />
      </div>
    )
  }
  if (state === 'offline') {
    return (
      <div className="auth-shell">
        <div className="auth-card">
          <Banner tone="bad" title="Cannot reach the Playkeeper panel">
            Check that the server is running, then reload this page.
          </Banner>
        </div>
      </div>
    )
  }
  if (state === 'setup') {
    return (
      <Setup
        onDone={(m) => {
          signedIn(m)
          navigate('/welcome', true)
        }}
      />
    )
  }
  if (state === 'login' || !me) {
    return (
      <Login
        onDone={(m) => {
          signedIn(m)
          navigate('/', true)
        }}
      />
    )
  }
  return <Shell me={me} route={route} onSignedOut={() => setState('login')} />
}

const nav: { to: Route; label: string; icon: string }[] = [
  { to: '/', label: 'Overview', icon: 'overview' },
  { to: '/console', label: 'Console', icon: 'console' },
  { to: '/players', label: 'Players', icon: 'players' },
  { to: '/world', label: 'World', icon: 'world' },
  { to: '/settings', label: 'Settings', icon: 'settings' },
]

function NavLink({ to, children, current }: { to: Route; children: ReactNode; current: boolean }) {
  return (
    <a
      href={to}
      aria-current={current ? 'page' : undefined}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
        e.preventDefault()
        navigate(to)
      }}
    >
      {children}
    </a>
  )
}

function Shell({ me, route, onSignedOut }: { me: Me; route: Route; onSignedOut: () => void }) {
  const poll = usePoll(() => get<ServerStatus>('/api/server'), 3000)
  const status = poll.data
  const agentDown = poll.error?.code === 'agent_unavailable'

  useEffect(() => {
    if (route === '/login' || route === '/setup') navigate('/', true)
  }, [route])

  useEffect(() => {
    if (status && !status.exists && route !== '/welcome' && route !== '/settings' && !status.operation) navigate('/welcome', true)
  }, [status, route])

  async function signOut() {
    try {
      await post('/api/auth/logout')
    } finally {
      onSignedOut()
      navigate('/login', true)
    }
  }

  const props: PageProps = { status, statusError: poll.error, refresh: poll.refresh, me }
  if (route === '/welcome') {
    return (
      <>
        <GlobalBanners status={status} agentDown={agentDown} />
        <Onboarding {...props} />
      </>
    )
  }
  let page: ReactNode
  switch (route) {
    case '/console':
      page = <ConsolePage {...props} />
      break
    case '/players':
      page = <PlayersPage {...props} />
      break
    case '/world':
      page = <WorldPage {...props} />
      break
    case '/settings':
      page = <SettingsPage {...props} onSignedOut={onSignedOut} />
      break
    default:
      page = <Overview {...props} />
  }
  return (
    <div className="app">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <aside className="sidebar">
        <div className="sidebar-top">
          <a className="brand" href="/" onClick={(e) => { e.preventDefault(); navigate('/') }}>
            <Logo /> Playkeeper
          </a>
        </div>
        <div className="server-chip" aria-live="polite">
          <span className="name">{status?.config?.motd ?? 'Minecraft server'}</span>
          {agentDown ? <span className="pill warn">Agent unavailable</span> : status ? <StatusPill phase={status.phase} /> : <span className="muted small">Checking…</span>}
        </div>
        <nav className="nav" aria-label="Main">
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} current={route === n.to}>
              <Icon name={n.icon} />
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          <span>
            Signed in as <strong>{me.user.username}</strong>
          </span>
          <button type="button" className="btn small" onClick={signOut}>
            Sign out
          </button>
        </div>
      </aside>
      <div className="main">
        <main id="main" className="content" tabIndex={-1}>
          <GlobalBanners status={status} agentDown={agentDown} />
          {page}
        </main>
        <Footer version={me.version} />
      </div>
    </div>
  )
}

export function Footer({ version }: { version?: string }) {
  return (
    <footer className="footer">
      <span>Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.</span>
      <span>Playkeeper {version ?? ''}</span>
    </footer>
  )
}

function GlobalBanners({ status, agentDown }: { status: ServerStatus | undefined; agentDown: boolean }) {
  const [dismissed, setDismissed] = useState<string>()
  if (agentDown) {
    return (
      <Banner tone="bad" title="The Playkeeper agent is not running">
        Playkeeper cannot see or control your Minecraft server right now, so no status is shown. On the server, check: <code>sudo systemctl status playkeeper-agent</code>
      </Banner>
    )
  }
  if (!status) return null
  const last = status.lastOperation
  const recentFailure = last && last.status === 'failed' && last.finishedAt && Date.now() - new Date(last.finishedAt).getTime() < 15 * 60_000 && dismissed !== last.id
  return (
    <>
      {status.offlineModeTest && (
        <Banner tone="bad" title="Test harness mode: offline mode is on">
          Anyone can join with any name. This is only for Playkeeper's automated protocol-bot tests and must never be used for a real server.
        </Banner>
      )}
      {status.phase === 'docker_unavailable' && (
        <Banner tone="warn" title={status.lastError ?? 'Docker is not responding'}>
          {status.lastErrorHint}
        </Banner>
      )}
      {recentFailure && (
        <Banner
          tone="bad"
          title={`${last.kind[0]?.toUpperCase()}${last.kind.slice(1)} failed: ${last.error ?? ''}`}
          action={
            <button type="button" className="btn small ghost" onClick={() => setDismissed(last.id)}>
              Dismiss
            </button>
          }
        >
          {last.hint}
        </Banner>
      )}
    </>
  )
}
