import { useEffect, useState } from 'react'

export type ServerTab = 'overview' | 'console' | 'players' | 'world' | 'plugins' | 'mods' | 'settings'
export const serverTabs: ServerTab[] = ['overview', 'console', 'players', 'world', 'plugins', 'mods', 'settings']

/** Pages under a tab, such as /servers/survival/world/pregen, and their paths under it. */
export type ServerSub = 'pregen' | 'packs' | 'browse' | 'schedules' | 'backup-rules' | 'backup-copies'
const serverSubs: Partial<Record<ServerTab, Partial<Record<ServerSub, string>>>> = {
  world: { pregen: 'pregen', packs: 'packs', 'backup-rules': 'backup-rules', 'backup-copies': 'backup-rules/copies' },
  plugins: { browse: 'browse' },
  mods: { browse: 'browse' },
  settings: { schedules: 'schedules' },
}
// Wave 7: the machine's Disk space page.
export type MachineSub = 'disk'

export type Route =
  | { name: 'home' }
  | { name: 'login' }
  | { name: 'setup' }
  | { name: 'welcome' }
  | { name: 'new-server' }
  // page is a page under Overview: "How it's running".
  | { name: 'server'; slug: string; tab: ServerTab; sub?: ServerSub; page?: 'running' }
  | { name: 'machine'; id: string; sub?: MachineSub }
  | { name: 'machine-settings'; id: string }
  | { name: 'settings' }
  | { name: 'account'; section?: 'two-factor' }
  | { name: 'more' }
  // Wave 7: bring a server back from its copies with its recovery key.
  | { name: 'recover' }
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
    case 'account':
      return second === 'two-factor' && !third ? { name: 'account', section: 'two-factor' } : { name: 'account' }
    case 'more':
      return { name: 'more' }
    case 'recover':
      return second ? { name: 'home' } : { name: 'recover' }
    case 'console':
    case 'players':
    case 'world':
      return { name: 'legacy', tab: first }
    case 'servers':
      if (second === 'new' && !third) return { name: 'new-server' }
      if (second && reSlug.test(second)) {
        if (third === 'running' && parts.length === 3) return { name: 'server', slug: second, tab: 'overview', page: 'running' }
        const tab = (third ?? 'overview') as ServerTab
        if (serverTabs.includes(tab) && parts.length <= 3) return { name: 'server', slug: second, tab }
        const rest = parts.slice(3).join('/')
        const subs = serverSubs[tab] ?? {}
        const sub = (Object.keys(subs) as ServerSub[]).find((k) => subs[k] === rest)
        if (sub) return { name: 'server', slug: second, tab, sub }
      }
      return { name: 'home' }
    case 'machines':
      if (second && /^[a-z2-9]{10}$/.test(second)) {
        if (!third) return { name: 'machine', id: second }
        if (third === 'settings' && parts.length === 3) return { name: 'machine-settings', id: second }
        if (third === 'disk' && parts.length === 3) return { name: 'machine', id: second, sub: 'disk' }
      }
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
    case 'server': {
      if (route.page === 'running') return `/servers/${route.slug}/running`
      const path = route.tab === 'overview' ? `/servers/${route.slug}` : `/servers/${route.slug}/${route.tab}`
      const sub = route.sub && serverSubs[route.tab]?.[route.sub]
      return sub ? `${path}/${sub}` : path
    }
    case 'machine':
      return route.sub ? `/machines/${route.id}/${route.sub}` : `/machines/${route.id}`
    case 'machine-settings':
      return `/machines/${route.id}/settings`
    case 'settings':
      return '/settings'
    case 'account':
      return route.section ? `/account/${route.section}` : '/account'
    case 'more':
      return '/more'
    case 'recover':
      return '/recover'
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
