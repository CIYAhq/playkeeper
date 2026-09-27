import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import type { HtmlTagDescriptor, Plugin } from 'vite'
import { faceCount, faceSvg } from './faces.ts'
import { iconCount, iconSvg } from './icons.ts'
import { demoMarker } from './marker.ts'

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))
const demoDir = here('.')
const swaps = new Map([
  [here('../api/client.ts'), here('./client.ts')],
  [here('../lib/demo.ts'), here('./parts.tsx')],
  [here('../lib/upload.ts'), here('./upload.ts')],
])

/** playkeeper.io's analytics (internal/site/settings.go), which counts the demo's visits too. */
const analytics = {
  src: 'https://analytics-c.ciya.so/oa.js',
  async: true,
  'data-key': 'oa_pk_tyJHnpyD4m-pl_XrUbi3maHu2Iqq87Uf',
  'data-collector': 'https://analytics-c.ciya.so',
}
const noReferrer = '<meta name="referrer" content="no-referrer" />'

/**
 * playkeeper.io/demo/ as search engines and link previews see it: the one page
 * of the demo in the site's sitemap (internal/site/seo.go). Its preview,
 * social.png, is drawn like the site's (test/e2e/ui/site-og.mjs) and served
 * from /demo/assets/, which robots.txt leaves open.
 */
const page = {
  url: 'https://playkeeper.io/demo/',
  title: "Live demo: Playkeeper's Minecraft server dashboard",
  name: 'Playkeeper live demo',
  description:
    "Try Playkeeper's Minecraft server dashboard in your browser, with sample servers and players. Click anything: nothing is real, and it resets every hour.",
  imageAlt: "Pip, Playkeeper's mascot, with the words Live demo: try the dashboard in your browser",
}
const shellTitle = '<title>Playkeeper</title>'

function pageTags(image: string): HtmlTagDescriptor[] {
  const meta = (key: 'name' | 'property', name: string, content: string): HtmlTagDescriptor => ({
    tag: 'meta',
    attrs: { [key]: name, content },
    injectTo: 'head',
  })
  return [
    meta('name', 'description', page.description),
    { tag: 'link', attrs: { rel: 'canonical', href: page.url }, injectTo: 'head' },
    meta('property', 'og:type', 'website'),
    meta('property', 'og:site_name', 'Playkeeper'),
    meta('property', 'og:title', page.name),
    meta('property', 'og:description', page.description),
    meta('property', 'og:url', page.url),
    meta('property', 'og:image', image),
    meta('property', 'og:image:width', '1200'),
    meta('property', 'og:image:height', '630'),
    meta('property', 'og:image:alt', page.imageAlt),
    meta('name', 'twitter:card', 'summary_large_image'),
    meta('name', 'twitter:title', page.name),
    meta('name', 'twitter:description', page.description),
    meta('name', 'twitter:image', image),
    {
      tag: 'noscript',
      injectTo: 'body-prepend',
      children: [
        { tag: 'h1', children: page.name },
        {
          tag: 'p',
          children:
            'Playkeeper\'s dashboard with sample servers, running in your browser, so it needs JavaScript. <a href="/">Read about Playkeeper</a> or <a href="/docs">the docs</a>.',
        },
      ],
    },
  ]
}

// The pictures the demo draws itself: the folder, how many, and number n.
const drawings: [string, number, (n: number) => string][] = [
  ['faces', faceCount, faceSvg],
  ['icons', iconCount, iconSvg],
]

/**
 * `vite build --mode demo`: every import of the API client, of lib/demo and
 * of lib/upload gets the demo's own, and the players' faces and plugins'
 * icons are written to faces/ and icons/. The demo's modules themselves still
 * reach the real ones, for ApiError and the upload's steps. The built page
 * loads the site's analytics, with the site's referrer policy: under
 * no-referrer, Firefox and Safari send its beacons with "Origin: null", which
 * its collector refuses. It also gets its own title, description, address
 * and link preview (page above).
 */
export function demoBuild(): Plugin {
  const preview = readFileSync(here('./social.png'))
  const previewFile = `assets/social-${createHash('sha256').update(preview).digest('hex').slice(0, 8)}.png`
  return {
    name: 'playkeeper-demo',
    enforce: 'pre',
    async resolveId(source, importer, options) {
      if (!importer || importer.startsWith(demoDir)) return null
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true })
      const swap = resolved && swaps.get(resolved.id.split('?')[0] ?? '')
      return swap ?? resolved
    },
    generateBundle() {
      for (const [folder, count, svg] of drawings) {
        for (let n = 0; n < count; n++) this.emitFile({ type: 'asset', fileName: `${folder}/${n}.svg`, source: svg(n) })
      }
      this.emitFile({ type: 'asset', fileName: previewFile, source: preview })
    },
    transformIndexHtml: {
      order: 'post',
      handler(html, ctx) {
        if (ctx.server) return html
        for (const want of [noReferrer, shellTitle]) {
          if (!html.includes(want)) throw new Error(`index.html has no ${want} for the demo to replace`)
        }
        return {
          html: html
            .replace(noReferrer, '<meta name="referrer" content="strict-origin-when-cross-origin" />')
            .replace(shellTitle, `<title>${page.title}</title>`),
          tags: [{ tag: 'script', attrs: analytics, injectTo: 'head' }, ...pageTags(page.url + previewFile)],
        }
      },
    },
    configureServer(server) {
      for (const [folder, count, svg] of drawings) {
        server.middlewares.use(`${server.config.base}${folder}`, (req, res, next) => {
          const n = Number(/^\/(\d+)\.svg$/.exec(req.url ?? '')?.[1] ?? NaN)
          if (!(n < count)) return next()
          res.setHeader('Content-Type', 'image/svg+xml')
          res.end(svg(n))
        })
      }
    },
  }
}

/** Every other build: fails if any of the demo made it into the output. */
export function noDemo(): Plugin {
  return {
    name: 'playkeeper-no-demo',
    apply: 'build',
    generateBundle(_, bundle) {
      for (const file of Object.values(bundle)) {
        const code = file.type === 'chunk' ? file.code : typeof file.source === 'string' ? file.source : ''
        const module = file.type === 'chunk' ? file.moduleIds.find((id) => id.startsWith(demoDir)) : undefined
        if (module || code.includes(demoMarker)) {
          this.error(`${file.fileName} contains the live demo (${module ?? demoMarker}); only vite build --mode demo may include src/demo.`)
        }
      }
    },
  }
}
