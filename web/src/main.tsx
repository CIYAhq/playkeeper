import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { ToastProvider } from '@/components/ui/toast'
import { parse } from '@/lib/router'
import { PackPage } from '@/pages/pack'
import { App } from './App'
import { PublicMapPage, publicMapToken } from './pages/public-map'
import './styles.css'

// Public pages never ask who is signed in: a friends' pack page, and the
// shared map at /map/<link token>.
const route = parse(window.location.pathname)
const mapToken = publicMapToken(window.location.pathname)

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          {route.name === 'pack' ? <PackPage token={route.token} /> : mapToken !== undefined ? <PublicMapPage token={mapToken} /> : <App />}
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
