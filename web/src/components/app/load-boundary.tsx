import { Component, type ReactNode } from 'react'
import { RotateCwIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

/**
 * Stands in for a page or tab whose code didn't load, or that failed to
 * show, so the rest of the dashboard stays on screen. Only a reload tries
 * again: a browser remembers a module that failed to load for as long as the
 * page is open. A new resetKey, such as the next page's, clears it.
 */
export class LoadBoundary extends Component<{ children: ReactNode; resetKey?: string }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  componentDidUpdate(prev: { resetKey?: string }) {
    if (this.state.failed && prev.resetKey !== this.props.resetKey) this.setState({ failed: false })
  }

  render() {
    if (!this.state.failed) return this.props.children
    return (
      <div role="alert" className="flex flex-1 flex-col items-center justify-center py-16 text-center">
        <h2 className="text-lg font-bold">{t('load.failed')}</h2>
        <p className="mt-1 max-w-[380px] text-sm text-muted-foreground">{t('load.failedBody')}</p>
        <Button className="mt-5" onClick={() => window.location.reload()}>
          <RotateCwIcon />
          {t('load.reload')}
        </Button>
      </div>
    )
  }
}

const reloadedAt = 'playkeeper.reloadedForCode'

/**
 * Reloads the page when Vite can't load a page's code, as after an update
 * replaced it. A page that still can't load within a minute of that reload
 * shows LoadBoundary's Reload instead of reloading again.
 */
export function reloadWhenCodeIsStale() {
  window.addEventListener('vite:preloadError', (e) => {
    try {
      if (Date.now() - Number(sessionStorage.getItem(reloadedAt) ?? 0) < 60_000) return
      sessionStorage.setItem(reloadedAt, String(Date.now()))
    } catch {
      return
    }
    e.preventDefault()
    window.location.reload()
  })
}
