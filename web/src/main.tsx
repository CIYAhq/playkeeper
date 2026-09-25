import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { ToastProvider } from '@/components/ui/toast'
import { App } from './App'
import { PublicMapPage, publicMapSlug } from './pages/public-map'
import './styles.css'

// Wave 6: the shared map at /map/<server> is public, so it never starts the signed-in app.
const mapSlug = publicMapSlug(window.location.pathname)

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          {mapSlug !== undefined ? <PublicMapPage slug={mapSlug} /> : <App />}
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
