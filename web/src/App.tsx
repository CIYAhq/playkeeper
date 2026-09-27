import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { ApiError, get, onUnauthorized, setCsrfToken } from '@/api/client'
import type { Me, SetupStatus } from '@/api/types'
import { useWorkspace, WorkspaceProvider } from '@/api/workspace'
import { Frame, FrameCard } from '@/components/app/frame'
import { LoadBoundary } from '@/components/app/load-boundary'
import { AppShell } from '@/components/app/shell'
import { PageSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { t } from '@/i18n'
import { navigate, useRoute, type Route } from '@/lib/router'
import { afterSignIn, signInPath } from '@/lib/templates'

// Each page's code loads the first time it shows, so the first screen doesn't
// wait for the others; once signed in, the rest load while the browser is idle.
const pages = {
  account: () => import('@/pages/account'),
  disk: () => import('@/pages/disk'),
  home: () => import('@/pages/home'),
  join: () => import('@/pages/join'),
  login: () => import('@/pages/login'),
  machine: () => import('@/pages/machine'),
  machineSettings: () => import('@/pages/machine-settings'),
  more: () => import('@/pages/more'),
  newServer: () => import('@/pages/new-server'),
  onboarding: () => import('@/pages/onboarding'),
  pack: () => import('@/pages/pack'),
  recover: () => import('@/pages/recover'),
  server: () => import('@/pages/server'),
  settings: () => import('@/pages/settings'),
}
const AccountPage = lazy(() => pages.account().then((m) => ({ default: m.AccountPage })))
const DiskPage = lazy(() => pages.disk().then((m) => ({ default: m.DiskPage })))
const HomePage = lazy(() => pages.home().then((m) => ({ default: m.HomePage })))
const JoinPage = lazy(() => pages.join().then((m) => ({ default: m.JoinPage })))
const LoginPage = lazy(() => pages.login().then((m) => ({ default: m.LoginPage })))
const MachinePage = lazy(() => pages.machine().then((m) => ({ default: m.MachinePage })))
const DashboardMachineOnly = lazy(() => pages.machine().then((m) => ({ default: m.DashboardMachineOnly })))
const MachineSettingsPage = lazy(() => pages.machineSettings().then((m) => ({ default: m.MachineSettingsPage })))
const MorePage = lazy(() => pages.more().then((m) => ({ default: m.MorePage })))
const NewServerPage = lazy(() => pages.newServer().then((m) => ({ default: m.NewServerPage })))
const AccountStep = lazy(() => pages.onboarding().then((m) => ({ default: m.AccountStep })))
const Onboarding = lazy(() => pages.onboarding().then((m) => ({ default: m.Onboarding })))
const PackPage = lazy(() => pages.pack().then((m) => ({ default: m.PackPage })))
const RecoverPage = lazy(() => pages.recover().then((m) => ({ default: m.RecoverPage })))
const ServerPage = lazy(() => pages.server().then((m) => ({ default: m.ServerPage })))
const GlobalSettingsPage = lazy(() => pages.settings().then((m) => ({ default: m.GlobalSettingsPage })))

function preloadPages() {
  for (const load of Object.values(pages)) void load().catch(() => {})
  void pages.server().then((m) => m.preloadTabs(), () => {})
}

/** While a first page's code loads, the same spinner as while signing in is checked. */
function Booting() {
  return (
    <Frame>
      <Spinner className="size-6 text-muted-foreground" aria-label={t('common.loading')} />
    </Frame>
  )
}

type AuthState = 'loading' | 'setup' | 'login' | 'ready' | 'offline'

export function App() {
  const route = useRoute()
  const [state, setState] = useState<AuthState>('loading')
  const [me, setMe] = useState<Me>()
  const [status, setStatus] = useState<SetupStatus>()

  const signedIn = useCallback((m: Me) => {
    setCsrfToken(m.csrfToken)
    setMe(m)
    setState('ready')
  }, [])

  const signedOut = useCallback(() => {
    setMe(undefined)
    setState('login')
    navigate(signInPath(window.location), true)
  }, [])

  // The invite page works without an account, so it skips signing in.
  const onJoin = route.name === 'join'

  useEffect(() => {
    if (onJoin) return
    let cancelled = false
    async function boot() {
      try {
        const s = await get<SetupStatus>('/api/setup/status')
        if (cancelled) return
        setStatus(s)
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
  }, [signedIn, signedOut, onJoin])

  if (route.name === 'join') {
    return (
      <LoadBoundary>
        <Suspense fallback={<Booting />}>
          <JoinPage
            code={route.code}
            onSignedIn={(m, to) => {
              signedIn(m)
              navigate(to ?? '/', true)
            }}
          />
        </Suspense>
      </LoadBoundary>
    )
  }

  switch (state) {
    case 'loading':
      return <Booting />
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
        <LoadBoundary>
          <Suspense fallback={<Booting />}>
            <AccountStep
              onDone={(m) => {
                signedIn(m)
                navigate('/welcome', true)
              }}
            />
          </Suspense>
        </LoadBoundary>
      )
    case 'login':
      return (
        <LoadBoundary>
          <Suspense fallback={<Booting />}>
            <LoginPage
              machine={status?.machine}
              version={status?.version}
              onDone={(m) => {
                signedIn(m)
                navigate(afterSignIn(window.location), true)
              }}
            />
          </Suspense>
        </LoadBoundary>
      )
    case 'ready':
      if (!me) return null
      return (
        <WorkspaceProvider me={me} onMe={signedIn} onSignedOut={signedOut}>
          <Routes route={route} />
        </WorkspaceProvider>
      )
    default: {
      const unreachable: never = state
      return unreachable
    }
  }
}

export function Routes({ route }: { route: Route }) {
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

  useEffect(() => {
    const idle = window.requestIdleCallback?.(preloadPages, { timeout: 5000 })
    const timer = idle === undefined ? window.setTimeout(preloadPages, 2000) : undefined
    return () => {
      if (idle !== undefined) window.cancelIdleCallback(idle)
      window.clearTimeout(timer)
    }
  }, [])

  if (route.name === 'welcome') {
    return (
      <LoadBoundary>
        <Suspense fallback={<Booting />}>
          <Onboarding />
        </Suspense>
      </LoadBoundary>
    )
  }
  return (
    <AppShell route={route}>
      <LoadBoundary resetKey={JSON.stringify(route)}>
        <Suspense fallback={<PageSkeleton />}>{page(route)}</Suspense>
      </LoadBoundary>
    </AppShell>
  )
}

function page(route: Route) {
  switch (route.name) {
    case 'home':
    case 'login':
    case 'setup':
    case 'welcome':
    case 'legacy':
    case 'join':
      return <HomePage />
    case 'new-server':
      return <NewServerPage key={route.machine ?? ''} machine={route.machine} />
    case 'server':
      return <ServerPage slug={route.slug} tab={route.tab} sub={route.sub} page={route.page} />
    case 'player':
      return <ServerPage slug={route.slug} tab="players" player={route.player} />
    case 'machine':
      return route.sub === 'disk' ? (
        <DiskPage id={route.id} />
      ) : (
        <DashboardMachineOnly id={route.id}>
          <MachinePage id={route.id} />
        </DashboardMachineOnly>
      )
    case 'machine-settings':
      return (
        <DashboardMachineOnly id={route.id}>
          <MachineSettingsPage id={route.id} />
        </DashboardMachineOnly>
      )
    case 'settings':
    case 'team':
    case 'addon-sources':
    case 'discord':
    case 'ai-agents':
    case 'machines':
    case 'machine-details':
      return <GlobalSettingsPage page={route} />
    case 'account':
      return <AccountPage section={route.section} />
    case 'more':
      return <MorePage />
    case 'pack':
      return <PackPage token={route.token} />
    case 'recover':
      return <RecoverPage />
    default: {
      const unreachable: never = route
      return unreachable
    }
  }
}
