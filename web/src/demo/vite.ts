import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'
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

// The pictures the demo draws itself: the folder, how many, and number n.
const drawings: [string, number, (n: number) => string][] = [
  ['faces', faceCount, faceSvg],
  ['icons', iconCount, iconSvg],
]

/**
 * `vite build --mode demo`: every import of the API client, of lib/demo and
 * of lib/upload gets the demo's own, and the players' faces and plugins'
 * icons are written to faces/ and icons/. The demo's modules themselves still
 * reach the real ones, for ApiError and the upload's steps.
 */
export function demoBuild(): Plugin {
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
