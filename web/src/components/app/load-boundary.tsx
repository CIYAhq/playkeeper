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

/** The script a page starts from, as its index.html names it. */
function entryScript(doc: Document) {
  return doc.querySelector('script[type="module"][src]')?.getAttribute('src') ?? undefined
}

/** Whether an update replaced this page's code: the page the server sends now starts from another script. */
async function codeReplaced() {
  const res = await fetch(import.meta.env.BASE_URL, { cache: 'no-store' })
  if (!res.ok) return false
  const now = entryScript(new DOMParser().parseFromString(await res.text(), 'text/html'))
  return now !== undefined && now !== entryScript(document)
}

/**
 * Reloads the page when Vite can't load a page's code because an update
 * replaced it. Code that didn't load for another reason shows LoadBoundary's
 * Reload instead: a phone lost its connection, or Safari stopped loading the
 * code of a page you're leaving, where a reload would win over the link you
 * pressed. A page that still can't load within a minute of that reload shows
 * the Reload too, instead of reloading again.
 */
export function reloadWhenCodeIsStale() {
  let checking = false
  window.addEventListener('vite:preloadError', () => {
    if (checking) return
    checking = true
    void codeReplaced()
      .then((replaced) => {
        if (!replaced || Date.now() - Number(sessionStorage.getItem(reloadedAt) ?? 0) < 60_000) return
        sessionStorage.setItem(reloadedAt, String(Date.now()))
        window.location.reload()
      })
      .catch(() => {})
      .finally(() => {
        checking = false
      })
  })
}
