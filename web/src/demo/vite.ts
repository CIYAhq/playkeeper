import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'
import { faceCount, faceSvg } from './faces.ts'
import { demoMarker } from './marker.ts'

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url))
const demoDir = here('.')
const swaps = new Map([
  [here('../api/client.ts'), here('./client.ts')],
  [here('../lib/demo.ts'), here('./parts.tsx')],
])

/**
 * `vite build --mode demo`: every import of the API client and of lib/demo
 * gets the demo's own, and the players' faces are written to faces/. The
 * demo's modules themselves still reach the real client, for ApiError.
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
      for (let n = 0; n < faceCount; n++) this.emitFile({ type: 'asset', fileName: `faces/${n}.svg`, source: faceSvg(n) })
    },
    configureServer(server) {
      server.middlewares.use(`${server.config.base}faces`, (req, res, next) => {
        const n = Number(/^\/(\d+)\.svg$/.exec(req.url ?? '')?.[1] ?? NaN)
        if (!(n < faceCount)) return next()
        res.setHeader('Content-Type', 'image/svg+xml')
        res.end(faceSvg(n))
      })
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
