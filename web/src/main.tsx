import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { LoadBoundary, reloadWhenCodeIsStale } from '@/components/app/load-boundary'
import { ToastProvider } from '@/components/ui/toast'
import { t } from '@/i18n'
import { parse, publicMapToken } from '@/lib/router'
import './styles.css'

// Public pages load none of the signed-in dashboard's code and never ask who
// is signed in: a friends' pack page, and the shared map at /map/<link token>.
const App = lazy(() => import('./App').then((m) => ({ default: m.App })))
const PackPage = lazy(() => import('@/pages/pack').then((m) => ({ default: m.PackPage })))
const PublicMapPage = lazy(() => import('@/pages/public-map').then((m) => ({ default: m.PublicMapPage })))

/** The page while its code loads, with the landmark and heading screen readers look for. */
function Loading() {
  return (
    <main id="main" aria-busy="true">
      <h1 className="sr-only">{t('brand.name')}</h1>
    </main>
  )
}

const route = parse(window.location.pathname)
const mapToken = publicMapToken(window.location.pathname)

reloadWhenCodeIsStale()
const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          <LoadBoundary>
            <Suspense fallback={<Loading />}>{route.name === 'pack' ? <PackPage token={route.token} /> : mapToken !== undefined ? <PublicMapPage token={mapToken} /> : <App />}</Suspense>
          </LoadBoundary>
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
