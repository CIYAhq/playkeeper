// playkeeper.io's custom events from the live demo, as the site's own pages
// send them (site/static/js/site.js): the demo's page loads the site's oa.js
// (vite.ts). Only the demo build has this module, so the dashboard people
// install sends nothing.

interface Tracker {
  track(name: string, properties: Record<string, string>): void
  flush(): void
}

/**
 * Sends a custom event with the page it happened on, if oa.js has loaded.
 * leaving: its link leaves the page, so it goes at once rather than with
 * oa.js's next batch, which a page on its way out can miss.
 */
export function count(name: string, properties: Record<string, string> = {}, leaving = false) {
  const oa = (globalThis as { oa?: Partial<Tracker> }).oa
  if (typeof oa?.track !== 'function') return
  oa.track(name, { ...properties, where: location.pathname })
  if (leaving) oa.flush?.()
}

/** Counts links out of the demo to the repository on GitHub, like its quiet prompt's Star on GitHub, or to /community, which sends people to its Discussions. */
export function countLinksOut(doc: Document) {
  const follow = (e: MouseEvent) => {
    if (e.type === 'auxclick' && e.button !== 1) return
    const a = e.target instanceof Element ? e.target.closest('a[href]') : null
    if (!(a instanceof HTMLAnchorElement)) return
    const repo = a.hostname === 'github.com' && /^\/CIYAhq\/playkeeper(\/|$)/.test(a.pathname)
    if (!repo && !(a.origin === location.origin && a.pathname === '/community')) return
    const part = repo ? a.pathname.split('/')[3] || 'repo' : 'community'
    count('github_clicked', { link: part === 'blob' || part === 'tree' ? 'file' : part }, true)
  }
  doc.addEventListener('click', follow, true)
  doc.addEventListener('auxclick', follow, true)
}
