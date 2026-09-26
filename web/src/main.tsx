import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { ToastProvider } from '@/components/ui/toast'
import { parse } from '@/lib/router'
import './styles.css'

// The public pack page loads none of the signed-in dashboard's code.
const App = lazy(() => import('./App').then((m) => ({ default: m.App })))
const PackPage = lazy(() => import('@/pages/pack').then((m) => ({ default: m.PackPage })))

// A friends' pack page is public: it never asks who is signed in.
const route = parse(window.location.pathname)

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          <Suspense>{route.name === 'pack' ? <PackPage token={route.token} /> : <App />}</Suspense>
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
