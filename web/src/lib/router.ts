import { useEffect, useState } from 'react'

export type Route = '/' | '/console' | '/players' | '/world' | '/settings' | '/setup' | '/login' | '/welcome'

const known: Route[] = ['/', '/console', '/players', '/world', '/settings', '/setup', '/login', '/welcome']

function current(): Route {
  const p = window.location.pathname.replace(/\/+$/, '') || '/'
  return (known as string[]).includes(p) ? (p as Route) : '/'
}

const listeners = new Set<() => void>()

export function navigate(to: Route, replace = false) {
  if (to === current() && !replace) return
  if (replace) window.history.replaceState(null, '', to)
  else window.history.pushState(null, '', to)
  listeners.forEach((fn) => fn())
  window.scrollTo(0, 0)
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(current)
  useEffect(() => {
    const update = () => setRoute(current())
    listeners.add(update)
    window.addEventListener('popstate', update)
    return () => {
      listeners.delete(update)
      window.removeEventListener('popstate', update)
    }
  }, [])
  return route
}
