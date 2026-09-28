import fs from 'node:fs'
import path from 'node:path'

// Which pages of the click-through a change touches, and how the pages it
// crawls are split between runners. It imports nothing of the crawler's at
// run time, so CI's plan job runs it with Node alone (plan.ts), and the specs
// share it.

export type Size = 'desktop' | 'phone'
export const sizeNames: Size[] = ['desktop', 'phone']

/** A page as the report names it: its route, and the faked state it was crawled in. */
export function where(route: string, view = 'live'): string {
  return view === 'live' ? route : `${route} (${view})`
}

/**
 * A page as the minimums, the costs and the page map name it: its route
 * without a server's slug, a machine's id, a player's name, a shared
 * template or a link's token.
 */
export function pageOf(route: string): string {
  return route
    .replace(/^\/servers\/new#template=.*$/, '/servers/new#template=*')
    .replace(/^\/servers\/(?!new(?:[#?]|$))[^/#?]+/, '/servers/*')
    .replace(/^\/servers\/\*\/players\/[^/]+/, '/servers/*/players/*')
    .replace(/^\/machines\/[^/]+/, '/machines/*')
    .replace(/^\/settings\/machines\/[^/]+/, '/settings/machines/*')
    .replace(/^\/(map|packs)\/[^/]+/, '/$1/*')
}

/** The crawlers of one size's run; each remembers what it pressed (crawl.ts), and only its own. */
export type CrawlerName = 'signed out' | 'second step' | 'signed in' | 'shared maps'

/** One page in one state, as one crawler crawls it. */
export interface Unit {
  crawler: CrawlerName
  route: string
  view: string
  /** A page with a minimum; the links that open nothing have no controls to count. */
  counted: boolean
  /** Its name in plans, costs and reports: where(pageOf(route), view), numbered when it repeats. */
  name: string
}

/** Names the pages of a run in the order it crawls them. */
export function named(units: Omit<Unit, 'name'>[]): Unit[] {
  const seen = new Map<string, number>()
  return units.map((u) => {
    const base = where(pageOf(u.route), u.view)
    const n = (seen.get(base) ?? 0) + 1
    seen.set(base, n)
    return { ...u, name: n > 1 ? `${base} #${n}` : base }
  })
}

/** A unit's page and state, read back from its name. */
export function parseName(name: string): { page: string; view: string } {
  const bare = name.replace(/ #\d+$/, '')
  const m = /^(.*) \(([^()]+)\)$/.exec(bare)
  return m ? { page: m[1] ?? '', view: m[2] ?? '' } : { page: bare, view: 'live' }
}

/**
 * What a run crawls: every page, or the pages a change touches in every
 * state they're crawled in plus the pages before them that hold the controls
 * they share (preludes), as they are.
 */
export type Selection = 'all' | { pages: string[]; preludes: string[] }

export function selects(selection: Selection, u: Pick<Unit, 'route' | 'view'>): boolean {
  if (selection === 'all') return true
  const page = pageOf(u.route)
  return selection.pages.includes(page) || (u.view === 'live' && selection.preludes.includes(page))
}

/**
 * The pages crawled before a page, as they are, so the controls it shares
 * with them (the sidebar, a server's header, the Settings and Account
 * sections' own) count there, as in a full crawl, and its own count against
 * its minimum.
 */
export function preludesOf(page: string): string[] {
  if (['/', '/login', '/setup', '/welcome'].includes(page) || /^\/(map|packs)\//.test(page)) return []
  const parent = [
    [/^\/servers\/\*\/./, '/servers/*'],
    [/^\/settings\/./, '/settings'],
    [/^\/account\/./, '/account'],
    [/^\/machines\/\*\/./, '/machines/*'],
  ].find(([re]) => (re as RegExp).test(page))?.[1] as string | undefined
  return parent ? ['/', parent] : ['/']
}

/** The seconds each page took in the last full run (the gate writes them); a page it hasn't seen counts as this. */
export const unknownCost = 60
export type Costs = Partial<Record<Size, Record<string, number>>>

/**
 * A runner's share, in seconds of the costs: with its setup (the install,
 * onboarding and the bots' scenario take about 6 minutes), the package before
 * it and the gate after, every page takes about half an hour.
 */
export const shardSeconds = 1080

/** The group a unit is crawled with: its crawler's pages, or a signed-in route's pages in all their states. */
function groupOf(u: Unit): string {
  return u.crawler === 'signed in' ? `signed in ${pageOf(u.route).replace(/#.*$/, '')}` : u.crawler
}

/**
 * Which of `shards` runners crawls each unit (1-based): whole groups, the
 * costliest first, each onto the least loaded runner. A group keeps a page's
 * faked states with the page, so their controls aren't pressed again from
 * scratch. Every runner works it out alike from the same pages.
 */
export function partition(units: Unit[], cost: (u: Unit) => number, shards: number): number[] {
  const groups = new Map<string, { first: number; cost: number; members: number[] }>()
  units.forEach((u, i) => {
    const key = groupOf(u)
    const g = groups.get(key) ?? { first: i, cost: 0, members: [] }
    g.cost += cost(u)
    g.members.push(i)
    groups.set(key, g)
  })
  const load = new Array<number>(Math.max(1, shards)).fill(0)
  const out = new Array<number>(units.length).fill(1)
  for (const g of [...groups.values()].sort((a, b) => b.cost - a.cost || a.first - b.first)) {
    const k = load.indexOf(Math.min(...load))
    load[k] = (load[k] ?? 0) + g.cost
    for (const i of g.members) out[i] = k + 1
  }
  return out
}

export function costOf(costs: Costs, size: Size): (u: Pick<Unit, 'name'>) => number {
  const table = costs[size] ?? {}
  return (u) => table[u.name] ?? unknownCost
}

/**
 * Seconds of the costs a selection has at a size, and whether it has a page
 * there that isn't only a prelude. A page without a cost at either size is
 * one CI's panel doesn't have (a Fabric server's Mods tab, a joined
 * machine), or one the last full run didn't know: when every page selected
 * is like that, both sizes look for them.
 */
export function estimate(costs: Costs, size: Size, selection: Selection): { seconds: number; pages: boolean } {
  let seconds = 0
  let pages = selection === 'all'
  for (const [name, s] of Object.entries(costs[size] ?? {})) {
    const { page, view } = parseName(name)
    if (!selects(selection, { route: page, view })) continue
    seconds += s
    if (selection !== 'all' && selection.pages.includes(page)) pages = true
  }
  if (selection !== 'all') {
    const known = new Set(sizeNames.flatMap((sz) => Object.keys(costs[sz] ?? {}).map((n) => parseName(n).page)))
    const unknown = selection.pages.filter((p) => !known.has(p))
    seconds += unknown.length * unknownCost
    if (unknown.length === selection.pages.length) pages = true
  }
  return { seconds, pages }
}

export interface Shard {
  size: Size
  shard: number
  of: number
}

/**
 * The groups a selection's pages fall into at a size, as the costs name
 * them: a page with its states is crawled on one runner (partition), so a
 * selection has no use for more runners than this.
 */
function groupsAt(costs: Costs, size: Size, selection: Selection): number {
  const groups = new Set<string>()
  for (const name of Object.keys(costs[size] ?? {})) {
    const { page, view } = parseName(name)
    if (selects(selection, { route: page, view })) groups.add(page.replace(/#.*$/, ''))
  }
  if (selection !== 'all') {
    const known = new Set(sizeNames.flatMap((sz) => Object.keys(costs[sz] ?? {}).map((n) => parseName(n).page)))
    for (const p of selection.pages) if (!known.has(p)) groups.add(p)
  }
  return groups.size
}

/** The runners a selection needs: one per `share` seconds of its pages at each size, no more than it has groups, none at a size it has no page at. */
export function shardsFor(costs: Costs, selection: Selection, share = shardSeconds): Shard[] {
  return sizeNames.flatMap((size) => {
    const { seconds, pages } = estimate(costs, size, selection)
    if (!pages) return []
    const of = Math.max(1, Math.min(Math.ceil(seconds / share), groupsAt(costs, size, selection)))
    return Array.from({ length: of }, (_, i) => ({ size, shard: i + 1, of }))
  })
}

/**
 * The modules under web/src that draw each page, besides what they import.
 * A server's tabs load lazily from its page (pages/server/index.tsx), so
 * each tab's page names its module. The gate fails a full run with a page
 * that isn't here, and plan.ts fails when a module listed here is gone or a
 * page module is in none of these pages and not in `uncrawled`.
 */
export const pageModules: Record<string, string[]> = {
  '/': ['pages/home.tsx'],
  '/login': ['pages/login.tsx'],
  '/setup': ['pages/onboarding.tsx'],
  '/welcome': ['pages/onboarding.tsx'],
  '/recover': ['pages/recover.tsx'],
  '/more': ['pages/more.tsx'],
  '/packs/*': ['pages/pack.tsx'],
  '/map/*': ['pages/public-map.tsx'],
  '/servers/new': ['pages/new-server.tsx'],
  '/servers/new#world': ['pages/new-server.tsx'],
  '/servers/new#template=*': ['pages/new-server.tsx'],
  '/servers/*': ['pages/server/index.tsx'],
  '/servers/*/console': ['pages/server/index.tsx', 'pages/server/console.tsx'],
  '/servers/*/players': ['pages/server/index.tsx', 'pages/server/players.tsx'],
  '/servers/*/players/*': ['pages/server/index.tsx', 'pages/server/profile.tsx'],
  '/servers/*/world': ['pages/server/index.tsx', 'pages/server/world.tsx'],
  '/servers/*/world/backup-rules': ['pages/server/index.tsx', 'pages/server/backups.tsx', 'pages/server/copies.tsx'],
  '/servers/*/world/backup-rules/copies': ['pages/server/index.tsx', 'pages/server/backups.tsx', 'pages/server/copies.tsx'],
  '/servers/*/world/pregen': ['pages/server/index.tsx', 'pages/server/world-pregen.tsx'],
  '/servers/*/world/packs': ['pages/server/index.tsx', 'pages/server/world-packs.tsx'],
  '/servers/*/map': ['pages/server/index.tsx', 'pages/server/map.tsx'],
  '/servers/*/plugins': ['pages/server/index.tsx', 'pages/server/plugins/index.tsx'],
  '/servers/*/plugins/browse': ['pages/server/index.tsx', 'pages/server/plugins/index.tsx'],
  '/servers/*/mods': ['pages/server/index.tsx', 'pages/server/plugins/index.tsx'],
  '/servers/*/mods/browse': ['pages/server/index.tsx', 'pages/server/plugins/index.tsx'],
  '/servers/*/files': ['pages/server/index.tsx', 'pages/server/files/index.tsx'],
  '/servers/*/files/plugins': ['pages/server/index.tsx', 'pages/server/files/index.tsx'],
  '/servers/*/file/server.properties': ['pages/server/index.tsx', 'pages/server/files/index.tsx'],
  '/servers/*/settings': ['pages/server/index.tsx', 'pages/server/settings.tsx'],
  '/servers/*/settings/schedules': ['pages/server/index.tsx', 'pages/server/settings.tsx', 'pages/server/schedules.tsx'],
  '/machines/*': ['pages/machine.tsx'],
  '/machines/*/settings': ['pages/machine.tsx', 'pages/machine-settings/index.tsx'],
  '/machines/*/disk': ['pages/disk.tsx'],
  '/settings': ['pages/settings.tsx'],
  '/settings/team': ['pages/settings.tsx'],
  '/settings/addon-sources': ['pages/settings.tsx'],
  '/settings/discord': ['pages/settings.tsx'],
  '/settings/ai-agents': ['pages/settings.tsx'],
  '/settings/machines': ['pages/settings.tsx'],
  '/settings/machines/*': ['pages/settings.tsx'],
  '/account': ['pages/account.tsx'],
  '/account/two-factor': ['pages/account.tsx'],
}

/** Page modules the click-through opens no page of: the invite page, Overview › How it's running, and the public server page, which only ports 443 and 80 serve (server-page.spec.ts checks it). */
export const uncrawled = ['pages/join.tsx', 'pages/server/running.tsx', 'pages/server-page.tsx']

/** Modules whose lazy imports are pages of their own, listed in pageModules, not part of every page they route to. */
const routers = new Set(['main.tsx', 'App.tsx', 'pages/server/index.tsx'])

/** The files of the click-through and the state it crawls in (onboarding, the bots' scenario), and its workflow. */
const crawlerFiles = [
  /^test\/e2e\/ui\/(crawl|crawl-page|fakes|addon-fixtures|modpack-fixtures|software-fixtures|helpers|clickthrough-plan|clickthrough-rules|plan)\.ts$/,
  /^test\/e2e\/ui\/(clickthrough|clickthrough-gate|onboarding)\.spec\.ts$/,
  /^test\/e2e\/ui\/(clickthrough-costs\.json|playwright\.config\.ts|package\.json|package-lock\.json)$/,
  /^test\/e2e\/ui\/fixtures\//,
  /^test\/e2e\/(scenario|pkclient)\.py$/,
  /^test\/e2e\/bot\//,
  /^scripts\/e2e\/played-state\.sh$/,
  /^\.github\/actions\/played-install\//,
  /^\.github\/workflows\/clickthrough\.yml$/,
]

/**
 * What makes the state the pages are crawled in, which a saved played state
 * was made by. A change to how it's saved or restored isn't here: the crawl
 * then starts from a saved state, which tries it.
 */
const setupFiles = [/^test\/e2e\/ui\/onboarding\.spec\.ts$/, /^test\/e2e\/(scenario|pkclient)\.py$/, /^test\/e2e\/bot\//]

/** Why a change has to be crawled after the onboarding and the bots instead of a saved played state, or undefined. */
export function freshSetup(changed: string[]): string | undefined {
  const file = changed.find((f) => setupFiles.some((re) => re.test(f)))
  return file && `${file} changes how the state the pages are crawled in is made`
}

/** How the dashboard is built and served: a change to any of these can change every page. */
const buildFiles = [/^web\/(index\.html|vite\.config\.ts|package\.json|package-lock\.json|tsconfig\.json|embed\.go)$/, /^web\/public\//]

const isTest = (file: string) => /\.test\.tsx?$/.test(file)

/** Why a changed file is the crawler's or how the dashboard is built, which a pull request's crawl samples with Home, or undefined. */
function crawlerOrBuild(file: string): string | undefined {
  if (crawlerFiles.some((re) => re.test(file))) return `${file} is part of the click-through or the state it crawls in`
  if (buildFiles.some((re) => re.test(file))) return `${file} changes how the dashboard is built or served`
  return undefined
}

interface Node {
  imports: string[]
  lazy: string[]
}
export type Graph = Map<string, Node>

const sourceFile = /\.(ts|tsx)$/

/** The runtime imports of each module under web/src, by path relative to it; type-only imports are left out. */
export function importGraph(src: string): Graph {
  const graph: Graph = new Map()
  const walk = (dir: string) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (sourceFile.test(e.name) && !e.name.endsWith('.d.ts')) graph.set(path.relative(src, p).split(path.sep).join('/'), { imports: [], lazy: [] })
    }
  }
  walk(src)
  const resolve = (from: string, spec: string): string | undefined => {
    let base: string
    if (spec.startsWith('@/')) base = spec.slice(2)
    else if (spec.startsWith('.')) base = path.posix.normalize(path.posix.join(path.posix.dirname(from), spec))
    else return undefined
    return [base, `${base}.ts`, `${base}.tsx`, `${base}/index.ts`, `${base}/index.tsx`].find((c) => graph.has(c))
  }
  for (const [file, node] of graph) {
    const text = fs.readFileSync(path.join(src, file), 'utf8')
    for (const m of text.matchAll(/^\s*(?:import|export)\s+(type\s+)?(?:[^'"`;]*?\s+from\s+)?['"]([^'"]+)['"]/gm)) {
      const to = !m[1] && resolve(file, m[2] ?? '')
      if (to) node.imports.push(to)
    }
    for (const m of text.matchAll(/\bimport\(\s*['"]([^'"]+)['"]\s*\)/g)) {
      const to = resolve(file, m[1] ?? '')
      if (to) node.lazy.push(to)
    }
  }
  return graph
}

/** Every module that runs for a page: its modules and what they import, lazily too except a router's pages. */
export function closure(graph: Graph, entries: string[]): Set<string> {
  const out = new Set<string>()
  const todo = [...entries]
  while (todo.length) {
    const f = todo.pop() as string
    if (out.has(f)) continue
    out.add(f)
    const node = graph.get(f)
    if (!node) continue
    todo.push(...node.imports)
    if (!routers.has(f)) todo.push(...node.lazy)
  }
  return out
}

export interface Affected {
  mode: 'none' | 'pages' | 'full'
  pages: string[]
  preludes: string[]
  /** Why, a line for each changed file that decided something. */
  why: string[]
}

const inPageOrder = (a: string, b: string) => Object.keys(pageModules).indexOf(a) - Object.keys(pageModules).indexOf(b)

/** Pages in the page map's order, with the pages crawled before them as they are. */
function withPreludes(pages: string[]): { pages: string[]; preludes: string[] } {
  const sorted = [...pages].sort(inPageOrder)
  return { pages: sorted, preludes: [...new Set(sorted.flatMap(preludesOf))].filter((p) => !pages.includes(p)).sort(inPageOrder) }
}

/**
 * A pull request's runners take this many seconds of pages each, at most
 * pullRequestRunners of them, so its crawl ends a few minutes after the fast
 * checks; the Release check crawls every page before each release. A page
 * with its states is never split, so the costliest ones take longer alone.
 */
export const pullRequestShardSeconds = 300
export const pullRequestRunners = 4

/** Every page loads the string table, so a change to it reaches the pages whose modules use the keys it changed. */
const stringTable = 'i18n/en.ts'

/**
 * What a pull request's crawl takes, which is never every page: the Release
 * check crawls them all before each release. A module under web/src takes
 * the pages it runs on (their modules and what they import); a change to
 * the string table takes the pages whose modules use the keys it changed
 * (usesOf), when they're given. A module more than a third of the pages run
 * on takes the first of them as their sample. A stylesheet, the app's shell
 * above the pages, a change to how the dashboard is built or to the crawler
 * and the state it crawls in takes Home, which shows the crawl still works
 * and stands for the pages they reach. When that's more than
 * pullRequestRunners runners' worth, the pages that list a changed module as
 * their own come first, then the other pages a changed page module runs on,
 * and the rest wait for the release.
 */
export function forPullRequest(changed: string[], graph: Graph, costs: Costs, keys?: string[], usesOf: (key: string) => string[] = () => []): Affected {
  const drawn = Object.entries(pageModules).map(([page, entries]) => ({ page, modules: closure(graph, entries.filter((e) => graph.has(e))) }))
  const reach = (rel: string) => drawn.filter((d) => d.modules.has(rel)).map((d) => d.page)
  const own = new Set<string>()
  const reached = new Set<string>()
  const touched = new Set<string>()
  const why: string[] = []
  // A module many pages run on is sampled by the first page whose own
  // modules list it, or else the first it runs on.
  const take = (what: string, pages: string[], into: Set<string>, rel?: string) => {
    if (pages.length > drawn.length / 3) {
      const sample = pages.find((p) => rel !== undefined && pageModules[p]?.includes(rel)) ?? (pages[0] as string)
      reached.add(sample)
      why.push(`${what} runs on ${pages.length} of the ${drawn.length} pages, so ${sample} stands for them`)
    } else {
      for (const p of pages) into.add(p)
      why.push(`${what}: ${pages.join(', ')}`)
    }
  }
  for (const file of changed) {
    const tooling = crawlerOrBuild(file)
    if (tooling) {
      reached.add('/')
      why.push(`${tooling}, so Home shows the crawl still works`)
      continue
    }
    if (!file.startsWith('web/src/') || isTest(file)) continue
    const rel = file.slice('web/src/'.length)
    if (rel.endsWith('.css')) {
      reached.add('/')
      why.push(`${file} is a stylesheet, which the page map doesn't follow, so Home stands for the pages it styles`)
      continue
    }
    if (!sourceFile.test(file) || rel.startsWith('demo/') || (rel === stringTable && keys)) continue
    touched.add(rel)
    const pages = reach(rel)
    if (pages.length) take(file, pages, rel.startsWith('pages/') ? own : reached, rel)
    else if (!uncrawled.includes(rel)) {
      reached.add('/')
      why.push(`${file} runs above the pages in pageModules (clickthrough-plan.ts), as the app's shell does, or on none it knows yet, so Home stands in`)
    }
  }
  for (const key of keys ?? []) {
    const users = usesOf(key)
    for (const m of users) touched.add(m)
    const pages = [...new Set(users.flatMap(reach))].sort(inPageOrder)
    if (pages.length) take(`the string ${key}`, pages, reached)
    else why.push(`the string ${key}: no page's modules name it`)
  }
  const rank = (p: string) => ((pageModules[p] ?? []).some((m) => touched.has(m)) ? 0 : own.has(p) ? 1 : 2)
  const ranked = [...new Set([...own, ...reached])].sort((a, b) => rank(a) - rank(b) || inPageOrder(a, b))
  const pages: string[] = []
  const left: string[] = []
  for (const p of ranked) {
    if (!pages.length || shardsFor(costs, withPreludes([...pages, p]), pullRequestShardSeconds).length <= pullRequestRunners) pages.push(p)
    else left.push(p)
  }
  if (left.length) why.push(`that's more than ${pullRequestRunners} runners' worth, so these wait for the release's full crawl: ${left.join(', ')}`)
  if (!pages.length) return { mode: 'none', pages: [], preludes: [], why: why.length ? why : ['no page of the dashboard changed'] }
  return { mode: 'pages', ...withPreludes(pages), why }
}

/**
 * The keys of the string table (web/src/i18n/en.ts) a change to it touched:
 * `diff` is its `git diff -U0`, `before` and `after` its text on either side.
 * A changed line counts under the key it belongs to in its version of the
 * file, its own line or a line of a value spread over several; comments and
 * blank lines count under none.
 */
export function keysChanged(diff: string, before: string, after: string): string[] {
  const keyLine = /^ {2}'([^']+)':/
  const inValue = /^( {4,}\S| {2}\},?$)/
  const keyAt = (lines: string[], i: number): string | undefined => {
    const own = keyLine.exec(lines[i] ?? '')
    if (own) return own[1]
    if (!inValue.test(lines[i] ?? '')) return undefined
    for (let j = i - 1; j >= 0; j--) {
      const m = keyLine.exec(lines[j] ?? '')
      if (m) return m[1]
      if (!/^ {4,}\S/.test(lines[j] ?? '')) return undefined
    }
    return undefined
  }
  const sides = { before: before.split('\n'), after: after.split('\n') }
  const keys = new Set<string>()
  for (const h of diff.matchAll(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/gm)) {
    const spans = [
      { lines: sides.before, from: Number(h[1]), count: h[2] === undefined ? 1 : Number(h[2]) },
      { lines: sides.after, from: Number(h[3]), count: h[4] === undefined ? 1 : Number(h[4]) },
    ]
    for (const { lines, from, count } of spans) {
      for (let n = from; n < from + count; n++) {
        const key = keyAt(lines, n - 1)
        if (key) keys.add(key)
      }
    }
  }
  return [...keys].sort()
}

/** What's wrong with the page map: modules that are gone, and page modules no page draws. */
export function pageMapProblems(graph: Graph): string[] {
  const problems: string[] = []
  for (const [page, entries] of Object.entries(pageModules)) for (const e of entries) if (!graph.has(e)) problems.push(`${page}: web/src/${e} doesn't exist`)
  const drawn = new Set(Object.values(pageModules).flatMap((entries) => [...closure(graph, entries)]))
  for (const file of graph.keys()) {
    if (file.startsWith('pages/') && !isTest(file) && !drawn.has(file) && !uncrawled.includes(file)) problems.push(`web/src/${file} is in no page of pageModules and not in uncrawled`)
  }
  for (const u of uncrawled) if (!graph.has(u)) problems.push(`uncrawled: web/src/${u} doesn't exist`)
  return problems
}
