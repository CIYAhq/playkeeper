import { gzipSync } from 'node:zlib'
import type { Plugin } from 'vite'

/** The parts of a built chunk the first loads are worked out from. */
export interface BuiltChunk {
  fileName: string
  code: string
  imports: string[]
  dynamicImports: string[]
  isEntry: boolean
  /** The pages under src/ that load lazily and are in this chunk, such as pages/login.tsx. */
  pages: string[]
}

export interface FirstLoad {
  page: string
  files: number
  bytes: number
  gzipBytes: number
}

/** The most the sign-in page may load before it shows, and the most any page may. */
export const budget = {
  signIn: { page: 'pages/login.tsx', bytes: 900_000, gzipBytes: 285_000 },
  anyPage: { bytes: 1_250_000, gzipBytes: 390_000 },
}

/**
 * The JavaScript each lazy page loads before it shows: the entry, then every
 * lazy chunk on its way in (the dashboard, a server's page, its tab), each
 * with the chunks it imports. Sizes are the files' own and gzip -9's.
 */
export function firstLoads(chunks: BuiltChunk[]): FirstLoad[] {
  const byFile = new Map(chunks.map((c) => [c.fileName, c]))
  const withImports = (start: BuiltChunk, into: Set<BuiltChunk>) => {
    const todo = [start]
    for (let c = todo.pop(); c; c = todo.pop()) {
      if (into.has(c)) continue
      into.add(c)
      for (const f of c.imports) {
        const next = byFile.get(f)
        if (next) todo.push(next)
      }
    }
    return into
  }
  const entry = chunks.find((c) => c.isEntry)
  if (!entry) return []
  // Each lazy chunk's first way in from the entry, breadth first.
  const loaded = new Map<BuiltChunk, Set<BuiltChunk>>([[entry, withImports(entry, new Set())]])
  const queue = [entry]
  for (let c = queue.shift(); c; c = queue.shift()) {
    const before = loaded.get(c) ?? new Set<BuiltChunk>()
    for (const f of c.dynamicImports) {
      const next = byFile.get(f)
      if (!next || loaded.has(next)) continue
      loaded.set(next, withImports(next, new Set(before)))
      queue.push(next)
    }
  }
  const gzip = new Map<BuiltChunk, number>()
  const out: FirstLoad[] = []
  for (const [c, files] of loaded) {
    let bytes = 0
    let gzipBytes = 0
    for (const f of files) {
      bytes += Buffer.byteLength(f.code)
      const g = gzip.get(f) ?? gzipSync(f.code, { level: 9 }).length
      gzip.set(f, g)
      gzipBytes += g
    }
    for (const page of c.pages) out.push({ page, files: files.size, bytes, gzipBytes })
  }
  return out.sort((a, b) => b.bytes - a.bytes || a.page.localeCompare(b.page))
}

const kB = (n: number) => `${(n / 1000).toLocaleString('en', { minimumFractionDigits: 1, maximumFractionDigits: 1 })} kB`

/** Each first load over the budget, in words. */
export function overBudget(loads: FirstLoad[]): string[] {
  const problems: string[] = []
  const signIn = loads.find((l) => l.page === budget.signIn.page)
  if (!signIn) problems.push(`no first load for ${budget.signIn.page}`)
  else if (signIn.bytes > budget.signIn.bytes || signIn.gzipBytes > budget.signIn.gzipBytes) {
    problems.push(`the sign-in page loads ${kB(signIn.bytes)} (${kB(signIn.gzipBytes)} gzipped), over its ${kB(budget.signIn.bytes)} (${kB(budget.signIn.gzipBytes)})`)
  }
  for (const l of loads) {
    if (l.bytes > budget.anyPage.bytes || l.gzipBytes > budget.anyPage.gzipBytes) {
      problems.push(`${l.page} loads ${kB(l.bytes)} (${kB(l.gzipBytes)} gzipped), over any page's ${kB(budget.anyPage.bytes)} (${kB(budget.anyPage.gzipBytes)})`)
    }
  }
  return problems
}

/** The dashboard's build: names the sign-in page's and the biggest first loads, and fails when one is over the budget. */
export function firstLoadBudget(): Plugin {
  return {
    name: 'playkeeper-first-load',
    apply: 'build',
    generateBundle(_, bundle) {
      const lazyPage = (id: string) => /\/src\/pages\//.test(id) && (this.getModuleInfo(id)?.dynamicImporters.length ?? 0) > 0
      const chunks = Object.values(bundle).flatMap((f) =>
        f.type === 'chunk' ? [{ ...f, pages: f.moduleIds.filter(lazyPage).map((id) => id.replace(/^.*\/src\//, '')) }] : [],
      )
      const loads = firstLoads(chunks)
      for (const l of [...loads.filter((x) => x.page === budget.signIn.page), ...loads.slice(0, 3)]) {
        this.info(`first load of ${l.page}: ${kB(l.bytes)}, ${kB(l.gzipBytes)} gzipped, ${l.files} files`)
      }
      const problems = overBudget(loads)
      if (problems.length > 0) this.error(`First load over budget (web/src/lib/first-load.ts): ${problems.join('; ')}.`)
    },
  }
}
