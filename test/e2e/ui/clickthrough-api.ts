import fs from 'node:fs'
import path from 'node:path'
import type { Graph } from './clickthrough-plan.ts'

// Which modules under web/src call the part of the dashboard's API a change
// to the Go code behind it reaches, so a pull request crawls their pages as
// for a change to those modules (forPullRequest): the declarations it
// changes, those in its package that use them, the panel's and the agent's
// handlers among those, the panel's routes (/api/…) that run them or pass
// requests on to the agent's (/v1/…) that do, and the modules that call
// those routes. Like the page map it reads the source as text, without
// types: an exported method or field counts where it's used on its own
// receiver, an unexported one on any value, since no other package's type
// has it; a route it can't follow reaches no page. The Release check
// crawls every page.

/** A top-level declaration of a Go file: a func, a type, or vars or consts, with its lines (1-based) and what it names. */
interface Decl {
  /** How code names it: a method as Type.name, anything else by its name. */
  keys: string[]
  /** The keys declared on each line of a group of vars or consts, or a struct's fields (Type.field), so a change to one line is a change to those. */
  byLine: Map<number, string[]>
  from: number
  to: number
  /** What its code names: a method or field of its receiver as Type.name, another package's as pkg.Name, one of some other value as ?.name. */
  words: Set<string>
  /** What each line names, for a struct, a group or a route table, whose lines are used one by one. */
  lineWords?: Map<number, Set<string>>
  /** The agent paths it names, as "/v1/catalog". */
  agentPaths: string[]
  /** Whether it holds lines of a route table. */
  table: boolean
}

/** The words of a line of Go code, strings and comments left out (Decl.words). */
function wordsOf(line: string, imported: Set<string>, receiver?: { name: string; type: string }): string[] {
  const code = line.replace(/"(?:[^"\\]|\\.)*"|`[^`]*`/g, '""').replace(/(^|\s)\/\/.*$/, '')
  return [...code.matchAll(/\b([A-Za-z_]\w*)((?:\.[A-Za-z_]\w*)*)/g)].flatMap((m) => {
    const first = m[1] as string
    const [member, ...rest] = m[2] ? m[2].slice(1).split('.') : []
    if (!member) return [first]
    const own = imported.has(first) ? `${first}.${member}` : receiver?.name === first ? `${receiver.type}.${member}` : `?.${member}`
    return [first, own, ...rest.map((r) => `?.${r}`)]
  })
}

export function goDecls(text: string): Decl[] {
  const lines = text.split('\n')
  const imported = new Set<string>()
  for (const m of text.matchAll(/^\s*(?:import\s+)?(\w+\s+)?"([\w./-]+)"$/gm)) imported.add(m[1]?.trim() || (m[2]?.split('/').at(-1) as string))
  const out: Decl[] = []
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? ''
    const func = /^func\s+(?:\((?:(\w+)\s+)?\*?(\w+)(?:\[[^\]]*\])?\)\s*)?(\w+)/.exec(line)
    const other = /^(type|var|const)\s+(\w+|\()/.exec(line)
    if (!func && !other) continue
    let end = i
    if (/[{(]$/.test(line.trimEnd())) {
      end = i + 1
      while (end < lines.length && !/^[})]/.test(lines[end] ?? '')) end++
    }
    const receiver = func?.[2] ? { name: func[1] ?? '', type: func[2] } : undefined
    const byLine = new Map<number, string[]>()
    let keys: string[]
    if (func) keys = [receiver ? `${receiver.type}.${func[3]}` : (func[3] as string)]
    else if (other?.[2] === '(') {
      for (let j = i + 1; j < end; j++) {
        const name = /^\t(\w+)/.exec(lines[j] ?? '')?.[1]
        if (name) byLine.set(j + 1, [name])
      }
      keys = [...byLine.values()].flat()
    } else {
      const name = other?.[2] as string
      keys = [name]
      if (other?.[1] === 'type' && /struct\s*\{$/.test(line.trimEnd())) {
        for (let j = i + 1; j < end; j++) {
          const fields = /^\t(\w+(?:,\s*\w+)*)\s+[^\s/]/.exec(lines[j] ?? '')?.[1]
          if (fields) byLine.set(j + 1, fields.split(/,\s*/).map((f) => `${name}.${f}`))
        }
      }
    }
    const body = lines.slice(i, end + 1)
    // A method's receiver isn't a use of its type.
    const header = receiver ? line.replace(/^func\s+\([^)]*\)/, 'func') : line
    const perLine = [header, ...body.slice(1)].map((l) => new Set(wordsOf(l, imported, receiver)))
    const table = body.some((l) => routeOf(l) !== undefined)
    out.push({
      keys,
      byLine,
      from: i + 1,
      to: end + 1,
      words: new Set(perLine.flatMap((w) => [...w])),
      lineWords: byLine.size || table ? new Map(perLine.map((w, j) => [i + 1 + j, w])) : undefined,
      agentPaths: [...body.join('\n').matchAll(/"(\/v1\/[^"]*)"/g)].map((p) => p[1] as string),
      table,
    })
    i = end
  }
  return out
}

/** The keys a change to `lines` of a declaration changes: the vars, consts or fields on those lines, else all of it; undefined lines is every line. */
function changedKeys(d: Decl, lines?: number[]): string[] {
  const touched = lines ? lines.filter((n) => n >= d.from && n <= d.to) : [d.from]
  if (!touched.length) return []
  if (!d.byLine.size || touched.includes(d.from) || !lines) return d.keys
  return touched.flatMap((n) => d.byLine.get(n) ?? [])
}

/** A route: the path it answers, the handlers its line names, and for the panel's, the agent path it passes requests on to. */
interface Route {
  path: string
  handlers: string[]
  agent?: string
  file: string
  line: number
}

/** A package's declarations by file (relative to the repository), the lines of its route tables and who names what. */
interface Package {
  name: string
  decls: Map<string, Decl[]>
  routes: Route[]
  /** The declarations naming each word, and the line, for one used line by line. */
  users: Map<string, { d: Decl; line?: number }[]>
}

function loadPackage(root: string, dir: string): Package {
  const pkg: Package = { name: path.basename(dir), decls: new Map(), routes: [], users: new Map() }
  const abs = path.join(root, dir)
  if (!fs.existsSync(abs)) return pkg
  for (const f of fs.readdirSync(abs).sort()) {
    if (!f.endsWith('.go') || f.endsWith('_test.go')) continue
    const file = `${dir}/${f}`
    const text = fs.readFileSync(path.join(abs, f), 'utf8')
    pkg.name = /^package (\w+)/m.exec(text)?.[1] ?? pkg.name
    const decls = goDecls(text)
    pkg.decls.set(file, decls)
    for (const d of decls) {
      const uses: [string, number | undefined][] = d.lineWords ? [...d.lineWords].flatMap(([n, ws]) => [...ws].map((w): [string, number] => [w, n])) : [...d.words].map((w) => [w, undefined])
      for (const [w, line] of uses) pkg.users.set(w, [...(pkg.users.get(w) ?? []), { d, line }])
    }
    text.split('\n').forEach((line, i) => {
      const route = routeOf(line)
      if (route) pkg.routes.push({ ...route, file, line: i + 1 })
    })
  }
  return pkg
}

/** A line of the panel's route table (a path under /api/ with a handler or an agent path) or the agent's ({"GET", "/v1/…", handler}). */
function routeOf(line: string): Omit<Route, 'file' | 'line'> | undefined {
  const handlers = [...line.matchAll(/\bh[A-Z]\w*/g)].map((h) => h[0])
  const agent = /^\s*\{"(?:GET|POST|PUT|PATCH|DELETE)", "(\/v1\/[^"]*)"/.exec(line)
  if (agent) return handlers.length ? { path: agent[1] as string, handlers } : undefined
  const api = /"(\/api\/[^"]*)"/.exec(line)
  const passedOn = /"(\/v1\/[^"]*)"/.exec(line)?.[1]
  return api && (handlers.length || passedOn) ? { path: api[1] as string, handlers, agent: passedOn } : undefined
}

/** The keys of what names `word`: of a struct or a group, what's on the lines that name it. A route table names nothing here. */
function keysUsing(pkg: Package, word: string): string[] {
  return (pkg.users.get(word) ?? []).flatMap(({ d, line }) => (d.table ? [] : (line !== undefined && line !== d.from && d.byLine.get(line)) || d.keys))
}

/**
 * The keys of the declarations that use `keys`, directly or through each
 * other, `keys` among them. An unexported method or field is this package's
 * whatever value it's used on; an exported one only on its receiver, since
 * another package's types have methods and fields of the same names.
 */
function usersOf(pkg: Package, keys: Iterable<string>): Set<string> {
  const out = new Set<string>()
  const todo = [...keys]
  while (todo.length) {
    const k = todo.pop() as string
    if (out.has(k)) continue
    out.add(k)
    const member = k.split('.')[1]
    todo.push(...keysUsing(pkg, k), ...(member && /^[a-z_]/.test(member) ? keysUsing(pkg, `?.${member}`) : []))
  }
  return out
}

/** The routes of a package that run what `keys` names: the handler or anything else on their line, and every route of a table that uses one of them elsewhere, as a helper all its routes are made with. */
function routesUsing(pkg: Package, keys: Set<string>): Route[] {
  const bare = (k: string) => k.split('.').at(-1) as string
  const handlers = new Set([...keys].map(bare).filter((k) => /^h[A-Z]/.test(k)))
  const out = new Set(pkg.routes.filter((r) => r.handlers.some((h) => handlers.has(h))))
  for (const [file, decls] of pkg.decls) {
    for (const d of decls) {
      if (!d.table || !d.lineWords) continue
      const mine = pkg.routes.filter((r) => r.file === file && r.line >= d.from && r.line <= d.to)
      for (const [line, words] of d.lineWords) {
        if (![...words].some((w) => keys.has(w) && !handlers.has(bare(w)))) continue
        const route = mine.filter((r) => r.line === line)
        for (const r of route.length ? route : mine) out.add(r)
      }
    }
  }
  return [...out]
}

/**
 * The paths the modules under web/src call: '/api/…' as it's written, and
 * serverApi, machineApi and filesApi's; ${…} is {}. A path worked out at
 * run time past a server or a machine, as /api/servers/{}/{}/{}, names no
 * route in particular and is left out.
 */
export function webCalls(src: string, graph: Graph): Map<string, string[]> {
  const bases: Record<string, string> = { serverApi: '/api/servers/{}', machineApi: '/api/machines/{}', filesApi: '/api/servers/{}/files' }
  const clean = (s: string) => s.replace(/\$\{[^}]*\}/g, '{}').replace(/\?.*$/, '')
  const out = new Map<string, string[]>()
  for (const file of graph.keys()) {
    if (/\.test\.tsx?$/.test(file) || file.startsWith('demo/')) continue
    const text = fs.readFileSync(path.join(src, file), 'utf8')
    const calls = [...text.matchAll(/(['"`])(\/api\/(?:[^'"`$\\]|\$\{[^}]*\})*)\1/g)].map((m) => clean(m[2] as string))
    for (const m of text.matchAll(/\b(serverApi|machineApi|filesApi)\(/g)) {
      const tail = secondArgument(text, (m.index ?? 0) + m[0].length)
      if (tail !== undefined) calls.push(clean(`${bases[m[1] as string]}${tail}`))
    }
    const named = calls.filter((c) => !/^\/api\/(servers|machines)\/\{\}(\/?\{\})+$/.test(c))
    if (named.length) out.set(file, [...new Set(named)])
  }
  return out
}

/** The second argument of the call whose arguments start at `from`, when it's a string (or none, ''); undefined when it's worked out at run time. */
function secondArgument(text: string, from: number): string | undefined {
  let depth = 0
  let quote = ''
  let start = -1
  for (let i = from; i < text.length; i++) {
    const c = text[i] as string
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = ''
      continue
    }
    if (c === "'" || c === '"' || c === '`') quote = c
    else if ('([{'.includes(c)) depth++
    else if (')]}'.includes(c) && depth > 0) depth--
    else if (c === ',' && depth === 0 && start < 0) start = i + 1
    else if ((c === ')' || (c === ',' && start >= 0)) && depth === 0) {
      if (start < 0) return ''
      const arg = text.slice(start, i).trim()
      const m = /^(['"`])((?:[^'"`\\$]|\$\{[^}]*\})*)\1$/.exec(arg)
      return m ? (m[2] as string) : undefined
    }
  }
  return undefined
}

/** Whether a call as webCalls writes it can reach a route's path: {} stands for one segment or part of one, a route's {param} for one, {rest...} for the rest. */
export function calls(call: string, route: string): boolean {
  const c = call.split('/')
  const r = route.replace(/\{\$\}$/, '').split('/')
  const rest = /^\{\w+\.\.\.\}$/.test(r.at(-1) ?? '')
  if (rest ? c.length < r.length - 1 : c.length !== r.length) return false
  const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return r.every((seg, i) => {
    if (rest && i === r.length - 1) return true
    const s = c[i] ?? ''
    if (/^\{\w+\}$/.test(seg)) return s !== ''
    return s.includes('{}') ? new RegExp(`^${s.split('{}').map(escape).join('[^/]*')}$`).test(seg) : s === seg
  })
}

/** A route of the API a changed Go file reaches, and the modules under web/src that call it. */
export interface ApiReach {
  path: string
  users: string[]
}

/**
 * For each changed Go file, what of the API it reaches. `changes` has the
 * lines each file has now that the change touched (added or replaced, or
 * next to lines it took out), or undefined for the whole file. A change in
 * the panel's or the agent's package reaches the handlers that use what it
 * changed; one elsewhere under internal/, those that use what it exports.
 */
export function apiReach(root: string, graph: Graph, changes: Map<string, number[] | undefined>): (file: string) => ApiReach[] {
  const packages = new Map<string, Package>()
  const load = (dir: string) => packages.get(dir) ?? (packages.set(dir, loadPackage(root, dir)).get(dir) as Package)
  const panel = load('internal/panel')
  const agent = load('internal/agent')
  const web = webCalls(path.join(root, 'web/src'), graph)
  return (file) => {
    if (!/^internal\/.*\.go$/.test(file) || file.endsWith('_test.go') || !changes.has(file)) return []
    const lines = changes.get(file)
    const pkg = load(path.posix.dirname(file))
    const changed = usersOf(pkg, (pkg.decls.get(file) ?? []).filter((d) => !d.table).flatMap((d) => changedKeys(d, lines)))
    const reached = new Map<Package, Set<string>>([
      [panel, new Set()],
      [agent, new Set()],
    ])
    if (reached.has(pkg)) reached.set(pkg, changed)
    else {
      const exported = [...changed].filter((k) => /^[A-Z]\w*$/.test(k)).map((k) => `${pkg.name}.${k}`)
      for (const p of [panel, agent]) reached.set(p, usersOf(p, exported.flatMap((q) => keysUsing(p, q))))
    }
    const direct = [...panel.routes, ...agent.routes].filter((r) => r.file === file && (!lines || lines.includes(r.line)))
    const agentPaths = new Set([...routesUsing(agent, reached.get(agent) as Set<string>), ...direct].map((r) => r.path).filter((p) => p.startsWith('/v1/')))
    const passing = [...panel.decls.values()].flat().filter((d) => !d.table && d.agentPaths.some((p) => agentPaths.has(p)))
    const inPanel = usersOf(panel, [...(reached.get(panel) as Set<string>), ...passing.flatMap((d) => d.keys)])
    const routes = [...routesUsing(panel, inPanel), ...panel.routes.filter((r) => r.agent && agentPaths.has(r.agent)), ...direct.filter((r) => r.path.startsWith('/api/'))]
    return [...new Set(routes.map((r) => r.path))].sort().map((p) => ({ path: p, users: [...web].filter(([, called]) => called.some((c) => calls(c, p))).map(([m]) => m) }))
  }
}
