import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { deflateSync } from 'node:zlib'

// Recorded answers to every add-on read the click-through makes, so it never
// waits on Modrinth or Hangar. fixtures/addons holds what the panel answered
// on 2026-09-26, through a logging proxy, for a fresh Paper 26.1.2 server
// with no add-ons: its folder and checks, the library's cards and each card's
// details, trimmed to the fields the dashboard reads (release notes left
// out). A search answers from those cards as though they were all each
// source lists, so there is never a second page. Details, plans and the jobs
// that follow them are worked out against the folder the Plugins tab last
// showed, the way the agent does, and icons are drawn here. A read nothing
// was recorded for gets no answer, and the harness reports it.

type Json = Record<string, unknown>

interface Key {
  source: string
  projectId: string
}

interface Card extends Key {
  slug: string
  name: string
  author?: string
  summary: string
  categories: string[]
  downloads: number
  updated: string
}

interface Version {
  versionId: string
  versionNumber: string
  channel: string
  published: string
  fileName?: string
  size?: number
  externalUrl?: string
}

interface Notice {
  kind: string
  params?: Record<string, string>
  message: string
  hint?: string
  url?: string
}

interface Step extends Key {
  action: string
  name: string
  versionNumber: string
  channel: string
  fileName: string
  size: number
  neededBy?: string
  was?: string
}

interface Plan {
  steps: Step[]
  manual: Notice[]
  blockers: Notice[]
  warnings: Notice[]
  ready: boolean
  fingerprint: string
}

type Unsealed = Omit<Plan, 'ready' | 'fingerprint'>

interface Details {
  card: Card
  latest?: Version
  notice?: Notice
  plan?: Plan
  planError?: Notice
}

/** The record Playkeeper keeps of an add-on it installed (api.Addon). */
interface Addon extends Key {
  name: string
  versionId: string
  versionNumber: string
  published: string
  fileName: string
  dependencyOf?: string
}

/** A record as the folder shows it: its file changed since it was installed, or gone. */
interface Kept {
  rec: Addon
  modified: boolean
  gone: boolean
}

/** The server as the fixtures see it. */
export interface World {
  /** Its add-on folder as the Plugins tab last showed it (GET …/addons). */
  addons: Json
  /** Whether its container runs, so that a finished job asks for a restart. */
  running: boolean
}

export interface Answer {
  status: number
  /** The JSON answer, or an icon's PNG. */
  body: unknown
  headers: Record<string, string>
}

/** How the agent answers an install or update: refusing to start it, or starting a job that ends like this. */
export type JobStart = { refused: Answer } | { ends: Json }

const here = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures', 'addons')
const load = (name: string): unknown => JSON.parse(readFileSync(path.join(here, name), 'utf8'))
const recorded = {
  addons: load('addons.json') as Json,
  checks: load('checks.json') as Json,
  cards: (load('search.json') as { cards: Card[] }).cards,
  details: load('details.json') as Record<string, Details>,
}

/** Whether a request only reads a server's add-ons: a GET under addons/, or what an update would do. */
export function isAddonRead(method: string, path: string): boolean {
  if (method === 'GET' || method === 'HEAD') return /^\/api\/servers\/\w+\/addons(\/.*)?$/.test(path)
  return method === 'POST' && /^\/api\/servers\/\w+\/addons\/update\/plan$/.test(path)
}

/** The recorded folder, with nothing in it yet. */
export function recordedFolder(): Json {
  return structuredClone(recorded.addons)
}

/** The answer to an add-on read (see isAddonRead) for `world`, or undefined when nothing was recorded for it. `body` is a POST's parsed body. */
export function answerRead(method: string, url: URL, body: unknown, world: World): Answer | undefined {
  const m = /^\/api\/servers\/\w+\/addons(\/.*)?$/.exec(url.pathname)
  if (!m) return undefined
  const rest = m[1] ?? ''
  if (method === 'POST') return rest === '/update/plan' ? updatePlan(body, world) : undefined
  if (rest === '') return json(200, recordedFolder())
  if (rest === '/checks') return json(200, { ...structuredClone(recorded.checks), checkedAt: new Date().toISOString() })
  if (rest === '/search') return search(url.searchParams, world)
  if (rest === '/icon') return icon(url.searchParams.get('url') ?? '')
  const project = /^\/project\/([^/]+)\/([^/]+)(\/removal)?$/.exec(rest)
  if (!project) return undefined
  const key = { source: pathValue(project[1] ?? ''), projectId: pathValue(project[2] ?? '') }
  const problem = addonKeyProblem(key)
  if (problem) return invalid(problem)
  return project[3] ? removal(key, world) : details(key, world)
}

/** The agent's check of the add-on a request names (parseAddonKey). */
export function addonKeyProblem(k: unknown): string | undefined {
  const { source, projectId } = (k ?? {}) as { source?: unknown; projectId?: unknown }
  if (source !== 'modrinth' && source !== 'hangar') return 'Add-ons come from Modrinth or Hangar.'
  if (typeof projectId !== 'string' || !/^[A-Za-z0-9._-]{1,64}$/.test(projectId) || projectId === '.' || projectId === '..') return 'That is not a valid project id.'
  return undefined
}

/** An install the dashboard confirmed (hAddonInstall, then Library.Install in the job); undefined when the add-on's details weren't recorded. */
export function installJob(body: unknown, world: World): JobStart | undefined {
  const req = decode(body, ['source', 'projectId', 'fingerprint', 'actor'])
  if ('refused' in req) return req
  const { source, projectId, fingerprint } = req.ok
  if (!isText(source) || !isText(projectId) || !isText(fingerprint)) return { refused: invalid('Invalid request body.') }
  const problem = addonKeyProblem(req.ok)
  if (problem) return { refused: invalid(problem) }
  if (!confirmed(fingerprint)) return { refused: notConfirmed() }
  const key = { source: String(source), projectId: String(projectId) }
  const d = recorded.details[id(key)]
  if (!d) return undefined
  const rec = kept(world).find((k) => sameKey(k.rec, key))?.rec
  if (rec) return { ends: failed({ kind: 'already_installed', params: { name: rec.name, version: rec.versionNumber }, message: `${rec.name} is already installed on this server (version ${rec.versionNumber}).`, hint: 'Use Update to change its version.' }) }
  if (d.notice) return { ends: failed(d.notice) }
  if (!d.latest) return undefined
  const planned = d.latest.externalUrl ? { plan: sealed({ steps: [], manual: [external(d.card.name, d.latest, folderName(world))], blockers: [], warnings: [] }) } : installPlan(d, world)
  if (!planned) return undefined
  return { ends: 'notice' in planned ? failed(planned.notice) : applied(planned.plan, String(fingerprint), world) }
}

/** An update the dashboard confirmed (hAddonUpdate, then Library.Update in the job); undefined when an add-on's details weren't recorded. */
export function updateJob(body: unknown, world: World): JobStart | undefined {
  const req = updateRequest(body, true)
  if ('refused' in req) return req
  const planned = planUpdate(req.ok, world)
  if (!planned) return undefined
  return { ends: 'notice' in planned ? failed(planned.notice) : applied(planned.plan, req.ok.fingerprint, world) }
}

// Reads.

/** How many cards the library asks each source for at a time. */
const perSource = 12

/** The library's categories and what each source calls them, '' where it has none (addons.categoryList). */
const categoryList: [id: string, modrinth: string, hangar: string][] = [
  ['admin', 'management', 'admin_tools'],
  ['chat', 'social', 'chat'],
  ['economy', 'economy', 'economy'],
  ['gameplay', 'game-mechanics', 'gameplay'],
  ['minigames', 'minigame', 'games'],
  ['world', 'worldgen', 'world_management'],
  ['protection', '', 'protection'],
  ['optimization', 'optimization', ''],
  ['utility', 'utility', 'misc'],
  ['library', 'library', 'dev_tools'],
  ['adventure', 'adventure', ''],
  ['technology', 'technology', ''],
  ['magic', 'magic', ''],
  ['storage', 'storage', ''],
  ['mobs', 'mobs', ''],
]
const categories = new Map(categoryList.map(([cat, modrinth, hangar]) => [cat, { modrinth, hangar }] as const))

/** One page of the library (hAddonSearch, Library.Browse). */
function search(q: URLSearchParams, world: World): Answer {
  const text = (q.get('q') ?? '').trim()
  if ([...text].length > 100) return invalid('Searches can be at most 100 characters.')
  if (!/^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u.test(text)) return invalid('Searches may not contain control characters.')
  const rawPage = q.get('page') ?? ''
  if (rawPage && (!/^[+-]?\d+$/.test(rawPage) || Number(rawPage) < 0 || Number(rawPage) > 800)) return invalid('That page of results is out of range.')
  const page = Number(rawPage || 0)
  const sort = q.get('sort') ?? ''
  if (!['', 'downloads', 'relevance', 'updated'].includes(sort)) return invalid('Results can be sorted by downloads, relevance or last update.')
  const category = q.get('category') ?? ''
  const tags = categories.get(category)
  if (category && !tags) return invalid("That is not one of the library's categories.")
  let more = false
  const lists = (['modrinth', 'hangar'] as const).map((source) => {
    const tag = tags?.[source]
    if (tag === '') return []
    const hits = recorded.cards.filter((c) => c.source === source && (!tag || c.categories.includes(tag)) && matches(c, text))
    if (sort !== 'relevance') hits.sort(sort === 'updated' ? byUpdated : byDownloads)
    more ||= (page + 1) * perSource < hits.length
    return hits.slice(page * perSource, (page + 1) * perSource)
  })
  const records = kept(world)
  const cards = merged(lists, sort).map((c) => ({ ...structuredClone(c), installed: records.some((k) => sameProject(k.rec, c)) }))
  return json(200, { cards, more, unanswered: [] })
}

/** A stand-in for the sources' full-text search: every word is somewhere on the card. */
function matches(c: Card, text: string): boolean {
  const card = `${c.name} ${c.slug} ${c.author ?? ''} ${c.summary}`.toLowerCase()
  return text.toLowerCase().split(/\s+/).every((word) => card.includes(word))
}

/** Each source's page taken in turns; an add-on listed on both appears once, from the listing with more downloads (mergeCards). */
function merged(lists: Card[][], sort: string): Card[] {
  const all: Card[] = []
  for (let i = 0; lists.some((l) => i < l.length); i++) for (const l of lists) if (l[i]) all.push(l[i] as Card)
  const best = new Map<string, number>()
  all.forEach((c, i) => {
    const j = best.get(norm(c.name))
    if (j === undefined || c.downloads > (all[j]?.downloads ?? 0)) best.set(norm(c.name), i)
  })
  const out = all.filter((c, i) => best.get(norm(c.name)) === i)
  if (sort !== 'relevance') out.sort(sort === 'updated' ? byUpdated : byDownloads)
  return out
}

const byDownloads = (a: Card, b: Card) => b.downloads - a.downloads
const byUpdated = (a: Card, b: Card) => Date.parse(b.updated) - Date.parse(a.updated)

/** One add-on's detail sheet (hAddonDetails). */
function details(key: Key, world: World): Answer | undefined {
  const asked = recorded.details[id(key)]
  if (!asked) return undefined
  const records = kept(world)
  const k = records.find((x) => sameKey(x.rec, asked.card)) ?? records.find((x) => sameProject(x.rec, asked.card))
  // Installed from its listing on the other source, whose versions are the ones that matter.
  const d = k && !sameKey(k.rec, asked.card) ? recorded.details[id(k.rec)] : asked
  if (!d) return undefined
  const latest = d.notice ? undefined : d.latest
  const out = { card: { ...d.card, installed: records.some((x) => sameProject(x.rec, d.card)) }, latest, notice: d.notice }
  if (k) {
    const updateAvailable = !!latest && !latest.externalUrl && newer(latest, k.rec)
    return json(200, { ...out, installed: k.rec, changed: k.modified || undefined, missing: k.gone || undefined, updateAvailable: updateAvailable || undefined })
  }
  if (!latest || latest.externalUrl) return json(200, out)
  const planned = installPlan(d, world)
  if (!planned) return undefined
  return json(200, 'notice' in planned ? { ...out, planError: planned.notice } : { ...out, plan: planned.plan })
}

/**
 * What removing an add-on involves (hAddonRemovePreview). The API leaves out
 * which add-ons each one requires, so only those installed for this one are
 * offered for removal with it, and none are said to need it.
 */
function removal(key: Key, world: World): Answer {
  const records = kept(world)
  const k = records.find((x) => sameKey(x.rec, key))
  if (!k) return refusal(notManaged(key))
  const orphans = records.filter((o) => o !== k && o.rec.source === key.source && o.rec.dependencyOf === key.projectId).map((o) => o.rec)
  return json(200, { addon: k.rec, neededBy: [], orphans, changed: k.modified, missing: k.gone })
}

/** What an update would do (hAddonUpdatePlan). */
function updatePlan(body: unknown, world: World): Answer | undefined {
  const req = updateRequest(body, false)
  if ('refused' in req) return req.refused
  const planned = planUpdate(req.ok, world)
  if (!planned) return undefined
  return 'notice' in planned ? refusal(planned.notice) : json(200, planned.plan)
}

const iconHosts = ['cdn.modrinth.com', 'hangarcdn.papermc.io']

/** The icon proxy (the panel's hAddonIcon, the agent's, Library.FetchIcon), drawing a stand-in for what the address would fetch. */
function icon(raw: string): Answer {
  if (!raw || Buffer.byteLength(raw) > 2048) return invalid('invalid icon address')
  let u: URL | undefined
  try {
    u = new URL(raw)
  } catch {
    u = undefined
  }
  if (!u?.host) return refusal({ kind: 'host_not_allowed', message: "Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from an invalid address." })
  if (u.protocol !== 'https:') return refusal({ kind: 'not_https', message: 'Playkeeper only loads icons over HTTPS.' })
  if (u.username || u.password || !iconHosts.includes(u.host)) return refusal({ kind: 'host_not_allowed', message: `Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from ${u.hostname}.` })
  return { status: 200, body: standInIcon(u.href), headers: { 'Content-Type': 'image/png', 'Cache-Control': 'private, max-age=86400' } }
}

// Plans.

/** What installing d's add-on would do on the server as its folder shows it (PlanInstall): the recorded plan, less the dependencies it has, blocked by files in the way. */
function installPlan(d: Details, world: World): { plan: Plan } | { notice: Notice } | undefined {
  if (d.planError) return { notice: d.planError }
  if (!d.plan) return undefined
  const present = kept(world).filter((k) => !k.gone)
  const has = new Set<string>()
  const steps = d.plan.steps.filter((s) => {
    const skip = !!s.neededBy && (has.has(s.neededBy) || present.some((k) => sameProject(k.rec, s)))
    if (skip) has.add(s.name)
    return !skip
  })
  const files = folderFiles(world)
  const blockers = [...d.plan.blockers]
  for (const s of steps) if (files.has(s.fileName)) add(blockers, fileExists(s, folderName(world)))
  if (steps.length === d.plan.steps.length && blockers.length === d.plan.blockers.length) return { plan: d.plan }
  return { plan: sealed({ ...d.plan, steps, blockers }) }
}

interface UpdateRequest {
  keys: Key[]
  changed: boolean
  fingerprint: string
}

/**
 * What updating would do (PlanUpdate), or the notice it fails with; the
 * newest versions are the recorded details'. Updates add no dependencies
 * here, and a newest version's notice isn't guessed at, as the agent words
 * it differently for updates.
 */
function planUpdate(req: UpdateRequest, world: World): { plan: Plan } | { notice: Notice } | undefined {
  const records = kept(world)
  const targets: Kept[] = []
  for (const key of req.keys) {
    const t = records.find((k) => sameKey(k.rec, key))
    if (!t) return { notice: notManaged(key) }
    if (!targets.includes(t)) targets.push(t)
  }
  if (req.keys.length === 0) targets.push(...records)
  const folder = folderName(world)
  const plan: Unsealed = { steps: [], manual: [], blockers: [], warnings: [] }
  const replacing: Kept[] = []
  for (const t of targets) {
    const d = recorded.details[id(t.rec)]
    if (!d || (d.notice && (req.keys.length > 0 || targets.length === 1))) return undefined
    const latest = d.notice ? undefined : d.latest
    if (!latest || (!newer(latest, t.rec) && !t.gone)) continue
    if (latest.externalUrl) {
      add(plan.manual, external(t.rec.name, latest, folder))
      continue
    }
    if (latest.channel !== 'release') add(plan.warnings, { kind: 'prerelease', params: { channel: latest.channel, name: t.rec.name, version: latest.versionNumber }, message: `${t.rec.name} ${latest.versionNumber} is a ${latest.channel} version and may be unstable.` })
    const neededBy = records.find((k) => k.rec.source === t.rec.source && k.rec.projectId === t.rec.dependencyOf)?.rec.name
    plan.steps.push({ action: 'update', source: t.rec.source, projectId: t.rec.projectId, name: t.rec.name, versionNumber: latest.versionNumber, channel: latest.channel, fileName: latest.fileName ?? '', size: latest.size ?? 0, neededBy, was: t.rec.versionNumber })
    replacing.push(t)
  }
  if (plan.steps.length === 0 && plan.manual.length === 0) {
    const only = targets.length === 1 ? targets[0]?.rec : undefined
    if (only) return { notice: { kind: 'up_to_date', params: { name: only.name, version: only.versionNumber }, message: `${only.name} is already at version ${only.versionNumber}.` } }
    return { notice: { kind: 'up_to_date', message: 'Every add-on is up to date.' } }
  }
  const files = folderFiles(world)
  const replaced = new Set(replacing.map((t) => t.rec.fileName))
  plan.steps.forEach((s, i) => {
    const t = replacing[i]
    if (!t) return
    if (files.has(s.fileName) && !replaced.has(s.fileName)) add(plan.blockers, fileExists(s, folder))
    const params = { file: t.rec.fileName, name: t.rec.name }
    if (t.gone) add(plan.warnings, { kind: 'not_found', params, message: `${t.rec.fileName} is missing from the ${folder} folder, so this installs it again.` })
    else if (t.modified && req.changed) add(plan.warnings, { kind: 'modified', params, message: `${t.rec.fileName} has changed since Playkeeper installed it; this replaces it as you asked.` })
    else if (t.modified) add(plan.blockers, { kind: 'modified', params, message: `${t.rec.fileName} has changed since Playkeeper installed it, so Playkeeper will not replace it.`, hint: 'If you changed it on purpose, manage it by hand in the file manager.' })
  })
  return { plan: sealed(plan) }
}

function sealed(p: Unsealed): Plan {
  return { ...p, ready: p.blockers.length === 0 && p.steps.length > 0, fingerprint: fingerprint(p) }
}

/**
 * The fingerprint of a plan worked out here. The agent's covers fields its
 * answers leave out, such as hashes, so a recorded plan keeps its own; like
 * the agent's, this one ignores the order of the steps.
 */
function fingerprint(p: Unsealed): string {
  const steps = [...p.steps].sort((a, b) => order(a.source, b.source) || order(a.projectId, b.projectId))
  return createHash('sha256').update(JSON.stringify({ steps, manual: p.manual, blockers: p.blockers })).digest('hex').slice(0, 32)
}

/** How a confirmed job ends once it has its plan (Library.apply, the agent's installAddons). */
function applied(p: Plan, confirmedAs: string, world: World): Json {
  if (confirmedAs !== p.fingerprint) return failed({ kind: 'plan_changed', message: 'What this would do has changed since you confirmed it.', hint: 'Review the new plan and confirm again.' })
  const blocker = p.blockers[0]
  if (blocker) return failed(blocker)
  if (p.steps.length === 0) return failed(p.manual[0] ?? { kind: 'up_to_date', message: 'There is nothing to install.' })
  const files = p.steps.map((s) => ({ name: s.name, versionNumber: s.versionNumber, was: s.was, neededBy: s.neededBy, size: s.size, received: s.size, state: 'verified' }))
  return { status: 'succeeded', phase: 'downloading', detail: { files, manual: p.manual.length > 0 ? p.manual : undefined, restartNeeded: world.running } }
}

/** A job that failed while planning, as the agent records it: the notice's message and hint, and the notice without its link. */
function failed(n: Notice): Json {
  return { status: 'failed', phase: 'planning', error: n.message, hint: n.hint, detail: { notice: { kind: n.kind, params: n.params, message: n.message, hint: n.hint } } }
}

function external(name: string, v: Version, folder: string): Notice {
  const url = v.externalUrl ?? ''
  let host = 'another site'
  try {
    host = new URL(url).hostname || host
  } catch {
    // The agent names no site for an address it can't read.
  }
  // Some Hangar versions are named after the project, e.g. Geyser's.
  const label = v.versionNumber && v.versionNumber !== name ? `${name} ${v.versionNumber}` : name
  return { kind: 'external_download', params: { folder, host, name, version: v.versionNumber }, message: `${label} is only offered on ${host}, so Playkeeper cannot install it for you.`, hint: `Download it from that page, then put it in the server's ${folder} folder.`, url: url || undefined }
}

function fileExists(s: Step, folder: string): Notice {
  return { kind: 'file_exists', params: { file: s.fileName, folder, name: s.name }, message: `The ${folder} folder already has a file named ${s.fileName}.`, hint: 'Playkeeper never overwrites other files. Rename or remove that file first.' }
}

function notManaged(k: Key): Notice {
  return { kind: 'not_managed', params: { project: k.projectId, source: k.source === 'hangar' ? 'Hangar' : 'Modrinth' }, message: 'Playkeeper did not install this add-on, so it cannot manage it.', hint: 'Scan the folder to let Playkeeper identify files added by hand.' }
}

/** Adds a notice unless one of the same kind says the same. */
function add(list: Notice[], n: Notice) {
  if (!list.some((o) => o.kind === n.kind && o.message === n.message)) list.push(n)
}

// Requests.

/** The body as the panel passes it on (an empty one as {}) and the agent decodes it, refusing fields it doesn't know. */
function decode(body: unknown, allowed: string[]): { ok: Json } | { refused: Answer } {
  if (body === undefined) return { ok: {} }
  if (typeof body !== 'object' || body === null || Array.isArray(body)) return { refused: invalid('Request body must be a JSON object.') }
  const unknown = Object.keys(body).find((k) => !allowed.includes(k))
  return unknown === undefined ? { ok: body as Json } : { refused: invalid(`Invalid request body: json: unknown field "${unknown}"`) }
}

/** An update or its plan as the agent reads it (updateKeys, confirmedPlan). */
function updateRequest(body: unknown, confirming: boolean): { ok: UpdateRequest } | { refused: Answer } {
  const req = decode(body, confirming ? ['addons', 'changed', 'fingerprint', 'actor'] : ['addons', 'changed', 'actor'])
  if ('refused' in req) return req
  const { addons, changed, fingerprint } = req.ok
  if ((addons != null && !Array.isArray(addons)) || (changed != null && typeof changed !== 'boolean') || !isText(fingerprint)) return { refused: invalid('Invalid request body.') }
  const keys = (addons ?? []) as unknown[]
  if (keys.length > 200) return { refused: invalid('At most 200 add-ons can be updated at once.') }
  for (const k of keys) {
    if (k !== null && (typeof k !== 'object' || Array.isArray(k))) return { refused: invalid('Invalid request body.') }
    const unknown = Object.keys((k ?? {}) as object).find((f) => f !== 'source' && f !== 'projectId')
    if (unknown !== undefined) return { refused: invalid(`Invalid request body: json: unknown field "${unknown}"`) }
    const problem = addonKeyProblem(k)
    if (problem) return { refused: invalid(problem) }
  }
  if (confirming && !confirmed(fingerprint)) return { refused: notConfirmed() }
  return { ok: { keys: keys as Key[], changed: changed === true, fingerprint: String(fingerprint ?? '') } }
}

/** A JSON string field, or one left out or null, which Go decodes as empty. */
function isText(v: unknown): boolean {
  return v == null || typeof v === 'string'
}

/** An install or update carries the fingerprint of the plan the user confirmed (confirmedPlan). */
function confirmed(fingerprint: unknown): boolean {
  return typeof fingerprint === 'string' && /^[0-9a-f]{32}$/.test(fingerprint)
}

function notConfirmed(): Answer {
  return json(400, { error: "This request doesn't include the plan you confirmed.", code: 'invalid_request', hint: 'Reload the page, check what it will do, and confirm again.' })
}

// Answers.

function json(status: number, body: unknown): Answer {
  return { status, body, headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' } }
}

function invalid(error: string): Answer {
  return json(400, { error, code: 'invalid_request' })
}

const statuses: Record<string, number> = {
  invalid_request: 400,
  bad_file_name: 400,
  icon_refused: 400,
  host_not_allowed: 400,
  not_https: 400,
  not_found: 404,
  not_managed: 404,
  rate_limited: 429,
  unreachable: 502,
  upstream_error: 502,
  redirect_refused: 502,
}

/** A library refusal as the agent answers it (addonError): its kind is the code, and its params are left out. */
function refusal(n: Notice): Answer {
  return json(statuses[n.kind] ?? 409, { error: n.message, code: n.kind, hint: n.hint })
}

// The folder.

/** The records Playkeeper keeps for the server, in the order it reads them: files it manages, changed or not, and those whose file is gone. */
function kept(world: World): Kept[] {
  const { files = [], missing = [] } = world.addons as { files?: { status?: string; addon?: Addon }[]; missing?: Addon[] }
  const out: Kept[] = [
    ...files.flatMap((f) => (f.addon && (f.status === 'managed' || f.status === 'modified') ? [{ rec: f.addon, modified: f.status === 'modified', gone: false }] : [])),
    ...missing.map((rec) => ({ rec, modified: false, gone: true })),
  ]
  return out.sort((a, b) => order(a.rec.name.toLowerCase(), b.rec.name.toLowerCase()) || order(a.rec.source, b.rec.source) || order(a.rec.projectId, b.rec.projectId))
}

function folderFiles(world: World): Set<string> {
  return new Set(((world.addons.files ?? []) as { fileName?: string }[]).map((f) => f.fileName ?? ''))
}

function folderName(world: World): string {
  return (world.addons.target as { folder?: string } | undefined)?.folder ?? 'plugins'
}

function id(k: Key): string {
  return `${k.source}:${k.projectId}`
}

function sameKey(a: Key, b: Key): boolean {
  return a.source === b.source && a.projectId === b.projectId
}

/** The same add-on: the same listing, or listed on both sources under one name (sameAddon). */
function sameProject(rec: Key & { name: string }, other: Key & { name: string }): boolean {
  const n = norm(other.name)
  return sameKey(rec, other) || (n !== '' && n === norm(rec.name))
}

/** A name reduced for matching across sources: "ViaVersion", "viaversion" and "Via-Version" are the same. */
function norm(s: string): string {
  return s.toLowerCase().replace(/[^\p{L}\p{Nd}]/gu, '')
}

/** Whether v is an update for rec: another version, published later (newer). */
function newer(v: Version, rec: Addon): boolean {
  return v.versionId !== rec.versionId && (rec.published.startsWith('0001-01-01') || Date.parse(v.published) > Date.parse(rec.published))
}

function order(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}

/** A path segment as Go's mux hands it over. */
function pathValue(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

// Icons.

const drawn = new Map<string, Buffer>()
const colours = [
  [0x2e, 0x7d, 0x32],
  [0x15, 0x65, 0xc0],
  [0x6a, 0x1b, 0x9a],
  [0xc6, 0x28, 0x28],
  [0xef, 0x6c, 0x00],
  [0x00, 0x83, 0x8f],
]

/** An 8×8 PNG in a colour the address picks, lighter in the middle, so tests never fetch or show an add-on's real icon. */
function standInIcon(address: string): Buffer {
  const done = drawn.get(address)
  if (done) return done
  let h = 2166136261
  for (const ch of address) h = Math.imul(h ^ (ch.codePointAt(0) ?? 0), 16777619) >>> 0
  const outer = colours[h % colours.length] ?? [0x61, 0x61, 0x61]
  const inner = outer.map((c) => Math.round(c + (255 - c) * 0.6))
  const rows: number[] = []
  for (let y = 0; y < 8; y++) {
    rows.push(0)
    for (let x = 0; x < 8; x++) rows.push(...(x > 1 && x < 6 && y > 1 && y < 6 ? inner : outer))
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(8, 0)
  header.writeUInt32BE(8, 4)
  header[8] = 8 // bits per sample
  header[9] = 2 // RGB
  const png = Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', header), chunk('IDAT', deflateSync(Buffer.from(rows))), chunk('IEND', Buffer.alloc(0))])
  drawn.set(address, png)
  return png
}

function chunk(type: string, data: Buffer): Buffer {
  const length = Buffer.alloc(4)
  length.writeUInt32BE(data.length)
  const body = Buffer.concat([Buffer.from(type, 'latin1'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(body))
  return Buffer.concat([length, body, crc])
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  return c >>> 0
})

function crc32(bytes: Buffer): number {
  let c = 0xffffffff
  for (const b of bytes) c = (crcTable[(c ^ b) & 0xff] ?? 0) ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}
