import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { CSPProvider } from '@base-ui/react/csp-provider'
import { ToastProvider } from '@/components/ui/toast'
import { App } from './App'
import './styles.css'

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      {/* The panel's Content Security Policy allows only its own style files. */}
      <CSPProvider disableStyleElements>
        <ToastProvider position="bottom-right" limit={3}>
          <App />
        </ToastProvider>
      </CSPProvider>
    </StrictMode>,
  )
}
