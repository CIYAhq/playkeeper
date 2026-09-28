import { useEffect, useState } from 'react'
import { scrollBehavior, scrollToSection } from './scroll'

export type ServerTab = 'overview' | 'console' | 'players' | 'world' | 'map' | 'plugins' | 'mods' | 'files' | 'settings'
export const serverTabs: ServerTab[] = ['overview', 'console', 'players', 'world', 'map', 'plugins', 'mods', 'files', 'settings']

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
  | { name: 'new-server'; machine?: string }
  // page is a page under Overview: "How it's running". path is a folder in
  // the Files tab (/servers/survival/files/plugins), or with file the file
  // open in its editor (/servers/survival/file/server.properties).
  | { name: 'server'; slug: string; tab: ServerTab; sub?: ServerSub; page?: 'running'; path?: string; file?: boolean }
  | { name: 'machine'; id: string; sub?: MachineSub }
  | { name: 'machine-settings'; id: string }
  | { name: 'settings' }
  | { name: 'account'; section?: 'two-factor' }
  | { name: 'more' }
  // Wave 7: bring a server back from its copies with its recovery key.
  | { name: 'recover' }
  // The pages of 0.2.0's single server; they open the first server's tab.
  | { name: 'legacy'; tab: ServerTab }
  // Wave 5: invite links, player profiles and the Settings sections.
  | { name: 'join'; code: string }
  | { name: 'player'; slug: string; player: string }
  | { name: 'team' }
  // machine is the one whose CurseForge key the page shows, from ?machine=; the dashboard's own when missing.
  | { name: 'addon-sources'; machine?: string }
  | { name: 'discord' }
  // A friends' pack page, public; token is "" for a link that can't be one.
  | { name: 'pack'; token: string }
  // Wave 8: AI agents, and the machines beyond the dashboard's own.
  | { name: 'ai-agents' }
  | { name: 'machines' }
  | { name: 'machine-details'; id: string }

const reSlug = /^[a-z0-9][a-z0-9-]{0,40}$/
const reCode = /^[A-Za-z0-9]{1,64}$/
export const rePlayerName = /^[A-Za-z0-9_]{3,16}$/
const rePackToken = /^[A-Za-z0-9]{22}$/
const reMachineId = /^[a-z2-9]{10}$/

/** The link token of the shared map at /map/<token>, or undefined on any other page. */
export function publicMapToken(pathname: string): string | undefined {
  const m = /^\/map\/([^/]*)\/?$/.exec(pathname)
  return m ? (m[1] ?? '') : undefined
}

/** A path in a server's files from the address's segments, each decoded; undefined when one isn't a name. */
function filesPath(segments: string[]): string | undefined {
  const names: string[] = []
  for (const s of segments) {
    let name: string
    try {
      name = decodeURIComponent(s)
    } catch {
      return undefined
    }
    if (name === '' || name === '.' || name === '..' || name.includes('/') || name.includes('\0')) return undefined
    names.push(name)
  }
  return names.join('/')
}

/** The Files tab's address for a folder, or for a file open in its editor. */
function filesHref(slug: string, path: string | undefined, file: boolean | undefined): string {
  const rest = (path ?? '').split('/').filter(Boolean).map(encodeURIComponent).join('/')
  if (file && rest) return `/servers/${slug}/file/${rest}`
  return rest ? `/servers/${slug}/files/${rest}` : `/servers/${slug}/files`
}

/** The machine a page is about, from ?machine= in the address: where a new server goes, or whose add-on sources show. */
function targetMachine(search: string): { machine?: string } {
  const id = new URLSearchParams(search).get('machine')
  return id && reMachineId.test(id) ? { machine: id } : {}
}

export function parse(pathname: string, search = ''): Route {
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
      if (second === 'addon-sources' && !third) return { name: 'addon-sources', ...targetMachine(search) }
      if (second === 'discord' && !third) return { name: 'discord' }
      if (second === 'ai-agents' && !third) return { name: 'ai-agents' }
      if (second === 'machines' && !third) return { name: 'machines' }
      if (second === 'machines' && third && reMachineId.test(third) && parts.length === 3) return { name: 'machine-details', id: third }
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
      if (second === 'new' && !third) return { name: 'new-server', ...targetMachine(search) }
      if (second && reSlug.test(second)) {
        if (third === 'running' && parts.length === 3) return { name: 'server', slug: second, tab: 'overview', page: 'running' }
        if (third === 'files' || third === 'file') {
          const path = filesPath(parts.slice(3))
          if (path === undefined || (third === 'file' && !path)) return { name: 'server', slug: second, tab: 'files' }
          return third === 'file' ? { name: 'server', slug: second, tab: 'files', path, file: true } : path ? { name: 'server', slug: second, tab: 'files', path } : { name: 'server', slug: second, tab: 'files' }
        }
        const tab = (third ?? 'overview') as ServerTab
        if (serverTabs.includes(tab) && parts.length <= 3) return { name: 'server', slug: second, tab }
        if (third === 'players' && fourth && rePlayerName.test(fourth) && parts.length === 4) {
          return { name: 'player', slug: second, player: fourth }
        }
        const rest = parts.slice(3).join('/')
        const subs = serverSubs[tab] ?? {}
        const sub = (Object.keys(subs) as ServerSub[]).find((k) => subs[k] === rest)
        if (sub) return { name: 'server', slug: second, tab, sub }
      }
      return { name: 'home' }
    case 'machines':
      if (second && reMachineId.test(second)) {
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
      return route.machine ? `/servers/new?machine=${route.machine}` : '/servers/new'
    case 'server': {
      if (route.page === 'running') return `/servers/${route.slug}/running`
      if (route.tab === 'files') return filesHref(route.slug, route.path, route.file)
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
    case 'join':
      return route.code ? `/join/${route.code}` : '/join'
    case 'player':
      return `/servers/${route.slug}/players/${route.player}`
    case 'team':
      return '/settings/team'
    case 'addon-sources':
      return route.machine ? `/settings/addon-sources?machine=${route.machine}` : '/settings/addon-sources'
    case 'discord':
      return '/settings/discord'
    case 'pack':
      return `/packs/${route.token}`
    case 'ai-agents':
      return '/settings/ai-agents'
    case 'machines':
      return '/settings/machines'
    case 'machine-details':
      return `/settings/machines/${route.id}`
    default: {
      const unreachable: never = route
      return unreachable
    }
  }
}

// Where the dashboard is served from: '' normally, '/demo' for the live demo.
// Routes and hrefs above are without it.
const base = import.meta.env.BASE_URL.replace(/\/$/, '')

function appPath(pathname: string): string {
  return base && pathname.startsWith(base) ? pathname.slice(base.length) || '/' : pathname
}

const listeners = new Set<() => void>()
// Told of every navigation, a link to the page you're on included; listeners only when the address changes.
const watchers = new Set<() => void>()

/** Pressing a link to the page you're on takes you back to its top, or to its section. */
function revisit(path: string) {
  watchers.forEach((fn) => fn())
  const hash = path.split('#')[1]
  const section = hash ? document.getElementById(hash) : null
  if (section) section.scrollIntoView({ block: 'start', behavior: scrollBehavior() })
  else window.scrollTo({ top: 0, behavior: scrollBehavior() })
  const focus = section?.matches('input, textarea, select, button, a[href], [tabindex]') ? section : document.getElementById('main')
  focus?.focus({ preventScroll: true })
}

/**
 * Whether the page may be left for a path, such as an editor with unsaved
 * changes: the guard may ask first, then navigate there itself once allowed.
 */
type LeaveGuard = (to: string) => boolean
let leaveGuard: LeaveGuard | undefined

/** Until the returned function removes it, leaving the page asks guard first, by a link or the browser's Back too. */
export function guardLeaving(guard: LeaveGuard): () => void {
  leaveGuard = guard
  return () => {
    if (leaveGuard === guard) leaveGuard = undefined
  }
}

// The address the page shows, for Back when a guard keeps the page.
let shown = typeof window === 'undefined' ? '' : window.location.pathname + window.location.search + window.location.hash

if (typeof window !== 'undefined') {
  // Registered before the router's own listener, so a kept page never changes.
  window.addEventListener('popstate', (e) => {
    const to = appPath(window.location.pathname) + window.location.search + window.location.hash
    if (leaveGuard && !leaveGuard(to)) {
      e.stopImmediatePropagation()
      window.history.pushState(null, '', shown)
      return
    }
    shown = window.location.pathname + window.location.search + window.location.hash
  })
}

export function navigate(to: Route | string, replace = false) {
  const target = typeof to === 'string' ? to : href(to)
  const path = base + target
  if (path === window.location.pathname + window.location.hash && !replace) {
    revisit(path)
    return
  }
  if (leaveGuard && !leaveGuard(target)) return
  // A #section of the page you're on scrolls there; another page starts at its top and jumps to its own section.
  const [pathname, hash] = path.split('#')
  const samePage = pathname === window.location.pathname
  const section = samePage && hash ? document.getElementById(hash) : null
  if (replace) window.history.replaceState(null, '', path)
  else window.history.pushState(null, '', path)
  shown = window.location.pathname + window.location.search + window.location.hash
  listeners.forEach((fn) => fn())
  watchers.forEach((fn) => fn())
  if (section) scrollToSection(section)
  else if (!(samePage && hash !== undefined)) window.scrollTo(0, 0)
}

/** Calls fn after each navigation inside the app, a link to the page you're on included, before the page scrolls; returns what stops it. */
export function onNavigate(fn: () => void): () => void {
  watchers.add(fn)
  return () => {
    watchers.delete(fn)
  }
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parse(appPath(window.location.pathname), window.location.search))
  useEffect(() => {
    const update = () => setRoute(parse(appPath(window.location.pathname), window.location.search))
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
    href: base + path,
    onClick: (e: React.MouseEvent<HTMLAnchorElement>) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
      e.preventDefault()
      navigate(path)
    },
  }
}
