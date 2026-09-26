import { useEffect, useState } from 'react'

export type ServerTab = 'overview' | 'console' | 'players' | 'world' | 'settings'
export const serverTabs: ServerTab[] = ['overview', 'console', 'players', 'world', 'settings']

export type Route =
  | { name: 'home' }
  | { name: 'login' }
  | { name: 'setup' }
  | { name: 'welcome' }
  | { name: 'new-server' }
  | { name: 'server'; slug: string; tab: ServerTab }
  | { name: 'machine'; id: string }
  | { name: 'settings' }
  | { name: 'more' }
  // The pages of 0.2.0's single server; they open the first server's tab.
  | { name: 'legacy'; tab: ServerTab }
  // Wave 5: invite links, player profiles, the team and Discord.
  | { name: 'join'; code: string }
  | { name: 'player'; slug: string; player: string }
  | { name: 'team' }
  | { name: 'discord' }

const reSlug = /^[a-z0-9][a-z0-9-]{0,40}$/
const reCode = /^[A-Za-z0-9]{1,64}$/
export const rePlayerName = /^[A-Za-z0-9_]{3,16}$/

export function parse(pathname: string): Route {
  const parts = pathname.replace(/\/+$/, '').split('/').filter(Boolean)
  const [first, second, third, fourth] = parts
  switch (first) {
    case undefined:
      return { name: 'home' }
    case 'login':
      return { name: 'login' }
    case 'setup':
      return { name: 'setup' }
    case 'welcome':
      return { name: 'welcome' }
    case 'join':
      return { name: 'join', code: second && reCode.test(second) && !third ? second : '' }
    case 'settings':
      if (second === 'team' && !third) return { name: 'team' }
      if (second === 'discord' && !third) return { name: 'discord' }
      return { name: 'settings' }
    case 'more':
      return { name: 'more' }
    case 'console':
    case 'players':
    case 'world':
      return { name: 'legacy', tab: first }
    case 'servers':
      if (second === 'new' && !third) return { name: 'new-server' }
      if (second && reSlug.test(second)) {
        const tab = (third ?? 'overview') as ServerTab
        if (serverTabs.includes(tab) && parts.length <= 3) return { name: 'server', slug: second, tab }
        if (third === 'players' && fourth && rePlayerName.test(fourth) && parts.length === 4) {
          return { name: 'player', slug: second, player: fourth }
        }
      }
      return { name: 'home' }
    case 'machines':
      if (second && /^[a-z2-9]{10}$/.test(second) && !third) return { name: 'machine', id: second }
      return { name: 'home' }
  }
  return { name: 'home' }
}

export function href(route: Route): string {
  switch (route.name) {
    case 'home':
      return '/'
    case 'login':
      return '/login'
    case 'setup':
      return '/setup'
    case 'welcome':
      return '/welcome'
    case 'new-server':
      return '/servers/new'
    case 'server':
      return route.tab === 'overview' ? `/servers/${route.slug}` : `/servers/${route.slug}/${route.tab}`
    case 'machine':
      return `/machines/${route.id}`
    case 'settings':
      return '/settings'
    case 'more':
      return '/more'
    case 'legacy':
      return `/${route.tab}`
    case 'join':
      return route.code ? `/join/${route.code}` : '/join'
    case 'player':
      return `/servers/${route.slug}/players/${route.player}`
    case 'team':
      return '/settings/team'
    case 'discord':
      return '/settings/discord'
    default: {
      const unreachable: never = route
      return unreachable
    }
  }
}

const listeners = new Set<() => void>()

/** Pressing a link to the page you're on takes you back to its top, or to its section. */
function revisit(path: string) {
  const hash = path.split('#')[1]
  const section = hash ? document.getElementById(hash) : null
  const behavior = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
  if (section) section.scrollIntoView({ block: 'start', behavior })
  else window.scrollTo({ top: 0, behavior })
  const focus = section?.matches('input, textarea, select, button, a[href], [tabindex]') ? section : document.getElementById('main')
  focus?.focus({ preventScroll: true })
}

export function navigate(to: Route | string, replace = false) {
  const path = typeof to === 'string' ? to : href(to)
  if (path === window.location.pathname + window.location.hash && !replace) {
    revisit(path)
    return
  }
  if (replace) window.history.replaceState(null, '', path)
  else window.history.pushState(null, '', path)
  listeners.forEach((fn) => fn())
  if (!path.includes('#')) window.scrollTo(0, 0)
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parse(window.location.pathname))
  useEffect(() => {
    const update = () => setRoute(parse(window.location.pathname))
    listeners.add(update)
    window.addEventListener('popstate', update)
    return () => {
      listeners.delete(update)
      window.removeEventListener('popstate', update)
    }
  }, [])
  return route
}

/** Props for an <a> that navigates inside the app without a page load. */
export function linkProps(to: Route) {
  return linkPath(href(to))
}

/** linkProps for a path, which may carry a #section. */
export function linkPath(path: string) {
  return {
    href: path,
    onClick: (e: React.MouseEvent<HTMLAnchorElement>) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
      e.preventDefault()
      navigate(path)
    },
  }
}
