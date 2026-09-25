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
  // A friends' pack page, public; token is "" for a link that can't be one.
  | { name: 'pack'; token: string }

const reSlug = /^[a-z0-9][a-z0-9-]{0,40}$/
const rePackToken = /^[A-Za-z0-9]{22}$/

export function parse(pathname: string): Route {
  const parts = pathname.replace(/\/+$/, '').split('/').filter(Boolean)
  const [first, second, third] = parts
  switch (first) {
    case undefined:
      return { name: 'home' }
    case 'login':
      return { name: 'login' }
    case 'setup':
      return { name: 'setup' }
    case 'welcome':
      return { name: 'welcome' }
    case 'settings':
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
      }
      return { name: 'home' }
    case 'machines':
      if (second && /^[a-z2-9]{10}$/.test(second) && !third) return { name: 'machine', id: second }
      return { name: 'home' }
    case 'packs':
      return { name: 'pack', token: second && rePackToken.test(second) && !third ? second : '' }
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
    case 'pack':
      return `/packs/${route.token}`
    default: {
      const unreachable: never = route
      return unreachable
    }
  }
}

const listeners = new Set<() => void>()

export function navigate(to: Route | string, replace = false) {
  const path = typeof to === 'string' ? to : href(to)
  if (path === window.location.pathname + window.location.hash && !replace) return
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
