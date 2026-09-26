import { useEffect, useState } from 'react'

export type ServerTab = 'overview' | 'console' | 'players' | 'world' | 'map' | 'plugins' | 'mods' | 'settings'
export const serverTabs: ServerTab[] = ['overview', 'console', 'players', 'world', 'map', 'plugins', 'mods', 'settings']

/** Pages under a tab, such as /servers/survival/world/pregen. */
export type ServerSub = 'pregen' | 'packs' | 'browse'
const serverSubs: Partial<Record<ServerTab, readonly ServerSub[]>> = {
  world: ['pregen', 'packs'],
  plugins: ['browse'],
  mods: ['browse'],
}

export type Route =
  | { name: 'home' }
  | { name: 'login' }
  | { name: 'setup' }
  | { name: 'welcome' }
  | { name: 'new-server' }
  // page is a page under Overview: "How it's running".
  | { name: 'server'; slug: string; tab: ServerTab; sub?: ServerSub; page?: 'running' }
  | { name: 'machine'; id: string }
  | { name: 'machine-settings'; id: string }
  | { name: 'settings' }
  | { name: 'account'; section?: 'two-factor' }
  | { name: 'more' }
  // The pages of 0.2.0's single server; they open the first server's tab.
  | { name: 'legacy'; tab: ServerTab }
  // Wave 5: invite links, player profiles and the Settings sections.
  | { name: 'join'; code: string }
  | { name: 'player'; slug: string; player: string }
  | { name: 'team' }
  | { name: 'addon-sources' }
  | { name: 'discord' }
  // A friends' pack page, public; token is "" for a link that can't be one.
  | { name: 'pack'; token: string }

const reSlug = /^[a-z0-9][a-z0-9-]{0,40}$/
const reCode = /^[A-Za-z0-9]{1,64}$/
export const rePlayerName = /^[A-Za-z0-9_]{3,16}$/
const rePackToken = /^[A-Za-z0-9]{22}$/

/** The link token of the shared map at /map/<token>, or undefined on any other page. */
export function publicMapToken(pathname: string): string | undefined {
  const m = /^\/map\/([^/]*)\/?$/.exec(pathname)
  return m ? (m[1] ?? '') : undefined
}

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
      if (second === 'addon-sources' && !third) return { name: 'addon-sources' }
      if (second === 'discord' && !third) return { name: 'discord' }
      return { name: 'settings' }
    case 'account':
      return second === 'two-factor' && !third ? { name: 'account', section: 'two-factor' } : { name: 'account' }
    case 'more':
      return { name: 'more' }
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
        if (third === 'players' && fourth && rePlayerName.test(fourth) && parts.length === 4) {
          return { name: 'player', slug: second, player: fourth }
        }
        const sub = fourth as ServerSub
        if (parts.length === 4 && serverSubs[tab]?.includes(sub)) return { name: 'server', slug: second, tab, sub }
      }
      return { name: 'home' }
    case 'machines':
      if (second && /^[a-z2-9]{10}$/.test(second)) {
        if (!third) return { name: 'machine', id: second }
        if (third === 'settings' && parts.length === 3) return { name: 'machine-settings', id: second }
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
      return route.sub ? `${path}/${route.sub}` : path
    }
    case 'machine':
      return `/machines/${route.id}`
    case 'machine-settings':
      return `/machines/${route.id}/settings`
    case 'settings':
      return '/settings'
    case 'account':
      return route.section ? `/account/${route.section}` : '/account'
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
    case 'addon-sources':
      return '/settings/addon-sources'
    case 'discord':
      return '/settings/discord'
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
