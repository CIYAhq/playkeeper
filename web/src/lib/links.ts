/**
 * A link a machine sent, to follow only when it is an absolute https: link to
 * another site: never a path or a page on the dashboard's own origin, where
 * a machine could serve a page or a script, and never javascript: or data:.
 */
export function externalLink(href: string | undefined): string | undefined {
  if (!href) return undefined
  let u: URL
  try {
    u = new URL(href)
  } catch {
    return undefined
  }
  return u.protocol === 'https:' && u.origin !== window.location.origin ? u.href : undefined
}
