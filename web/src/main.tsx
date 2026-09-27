import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { ToastProvider } from '@/components/ui/toast'
import { parse } from '@/lib/router'
import { PackPage } from '@/pages/pack'
import { App } from './App'
import './styles.css'

// A friends' pack page is public: it never asks who is signed in.
const route = parse(window.location.pathname)

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          {route.name === 'pack' ? <PackPage token={route.token} /> : <App />}
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
