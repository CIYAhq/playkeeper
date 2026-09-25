import { useCallback, useEffect, useState } from 'react'
import { ApiError, get, onUnauthorized, setCsrfToken } from '@/api/client'
import type { Me } from '@/api/types'
import { useWorkspace, WorkspaceProvider } from '@/api/workspace'
import { Frame, FrameCard } from '@/components/app/frame'
import { AppShell } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { t } from '@/i18n'
import { navigate, useRoute, type Route } from '@/lib/router'
import { HomePage } from '@/pages/home'
import { LoginPage } from '@/pages/login'
import { MachinePage } from '@/pages/machine'
import { MorePage } from '@/pages/more'
import { NewServerPage } from '@/pages/new-server'
import { RecoverPage } from '@/pages/recover'
import { AccountStep, Onboarding } from '@/pages/onboarding'
import { ServerPage } from '@/pages/server'
import { GlobalSettingsPage } from '@/pages/settings'

type AuthState = 'loading' | 'setup' | 'login' | 'ready' | 'offline'

export function App() {
  const route = useRoute()
  const [state, setState] = useState<AuthState>('loading')
  const [me, setMe] = useState<Me>()

  const signedIn = useCallback((m: Me) => {
    setCsrfToken(m.csrfToken)
    setMe(m)
    setState('ready')
  }, [])

  const signedOut = useCallback(() => {
    setMe(undefined)
    setState('login')
    navigate('/login', true)
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
        if (e instanceof ApiError && e.status === 401) signedOut()
        else setState('offline')
      }
    }
    void boot()
    const off = onUnauthorized(signedOut)
    return () => {
      cancelled = true
      off()
    }
  }, [signedIn, signedOut])

  switch (state) {
    case 'loading':
      return (
        <Frame>
          <Spinner className="size-6 text-muted-foreground" aria-label={t('common.loading')} />
        </Frame>
      )
    case 'offline':
      return (
        <Frame>
          <FrameCard>
            <h1 className="text-xl font-bold">{t('agentDown.unreachable')}</h1>
            <p className="mt-2 text-sm text-muted-foreground">{t('agentDown.unreachableBody')}</p>
            <Button className="mt-5" onClick={() => window.location.reload()}>
              {t('agentDown.reload')}
            </Button>
          </FrameCard>
        </Frame>
      )
    case 'setup':
      return (
        <AccountStep
          onDone={(m) => {
            signedIn(m)
            navigate('/welcome', true)
          }}
        />
      )
    case 'login':
      return (
        <LoginPage
          onDone={(m) => {
            signedIn(m)
            navigate('/', true)
          }}
        />
      )
    case 'ready':
      if (!me) return null
      return (
        <WorkspaceProvider me={me} onSignedOut={signedOut}>
          <Routes route={route} />
        </WorkspaceProvider>
      )
    default: {
      const unreachable: never = state
      return unreachable
    }
  }
}

function Routes({ route }: { route: Route }) {
  const { servers } = useWorkspace()

  useEffect(() => {
    if (route.name === 'login' || route.name === 'setup') navigate('/', true)
  }, [route.name])

  // 0.2.0 had one server with pages at /console, /players and /world.
  useEffect(() => {
    if (route.name !== 'legacy' || !servers) return
    const first = servers[0]
    navigate(first ? { name: 'server', slug: first.slug, tab: route.tab } : { name: 'home' }, true)
  }, [route, servers])

  if (route.name === 'welcome') return <Onboarding />
  return <AppShell route={route}>{page(route)}</AppShell>
}

function page(route: Route) {
  switch (route.name) {
    case 'home':
    case 'login':
    case 'setup':
    case 'welcome':
    case 'legacy':
      return <HomePage />
    case 'new-server':
      return <NewServerPage />
    case 'server':
      return <ServerPage slug={route.slug} tab={route.tab} sub={route.sub} />
    case 'machine':
      return <MachinePage id={route.id} />
    case 'settings':
      return <GlobalSettingsPage />
    case 'more':
      return <MorePage />
    case 'recover':
      return <RecoverPage />
    default: {
      const unreachable: never = route
      return unreachable
    }
  }
}
