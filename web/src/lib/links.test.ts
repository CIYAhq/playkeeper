// @vitest-environment happy-dom
// @vitest-environment-options {"url": "https://play.example.net/servers/survival/plugins"}
import { describe, expect, it } from 'vitest'
import { externalLink } from './links'

describe('externalLink', () => {
  it.each([
    { name: 'a path on the dashboard', href: '/api/servers/rstuvwxyzq/offsite/recovery-key', link: undefined },
    { name: 'a page on the dashboard, written out', href: 'https://play.example.net/api/servers/rstuvwxyzq/offsite/recovery-key', link: undefined },
    { name: 'a page on the dashboard, in capitals', href: 'HTTPS://PLAY.EXAMPLE.NET/api/servers/rstuvwxyzq/icon', link: undefined },
    { name: 'a link without its scheme', href: '//modrinth.com/plugin/chunky', link: undefined },
    { name: 'a script', href: 'javascript:alert(1)', link: undefined },
    { name: 'data', href: 'data:text/html,<script>alert(1)</script>', link: undefined },
    { name: 'plain http', href: 'http://modrinth.com/plugin/chunky', link: undefined },
    { name: 'nothing', href: undefined, link: undefined },
    { name: 'an add-on page on its site', href: 'https://modrinth.com/plugin/chunky', link: 'https://modrinth.com/plugin/chunky' },
  ])('follows $name only when it is an https: link to another site', ({ href, link }) => {
    expect(externalLink(href)).toBe(link)
  })
})
