import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { iconAnswer, invalid, json, type Answer } from './addon-fixtures'

// Recorded answers to every modpack read the create flow makes, so the
// click-through never waits on Modrinth or CurseForge. fixtures/modpacks/
// modrinth.json holds what the dev panel answered on 2026-09-26: the Modrinth
// library at each of the picker's sorts (the first six packs of each), each
// pack's details, with its versions cut to the ten newest and the one a new
// server gets, and that version's preview. A pack whose preview failed that
// day is left out. No CurseForge key was at hand, so curseforge.json holds
// what Playkeeper's own library answers for the made-up Example Fabric Pack of
// internal/modpacks/curseforge/testdata, served by the tests' fake CurseForge
// (its sizes are the small files those tests generate); the other made-up
// packs have no files there and are left out. A search answers from a
// source's recorded packs as though they were all it lists, and icons are
// drawn like add-ons'. A read nothing was recorded for gets no answer, and
// the harness reports it.

type Json = Record<string, unknown>

type Source = 'modrinth' | 'curseforge'

interface Card {
  projectId: string
  slug: string
  name: string
  author?: string
  summary: string
  downloads: number
  updated: string
  types: string[]
  minecraftVersions: string[]
}

interface Recorded {
  /** Each sort's first page: how many packs the source said match, and the ids of those kept, in its order. */
  searches: Record<string, { total: number; cards: string[] }>
  /** The packs as the searches listed them, by project id. */
  cards: Record<string, Card>
  details: Record<string, Json & { slug: string }>
  /** Previews by project id and version id: "<project>/<version>". */
  previews: Record<string, Json>
}

/** The machine as the modpack fixtures see it. */
export interface PackWorld {
  /** Whether it offers CurseForge, which takes a key (Settings › Add-on sources). */
  curseforge: boolean
}

const here = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures', 'modpacks')
const recorded: Record<Source, Recorded> = {
  modrinth: JSON.parse(readFileSync(path.join(here, 'modrinth.json'), 'utf8')) as Recorded,
  curseforge: JSON.parse(readFileSync(path.join(here, 'curseforge.json'), 'utf8')) as Recorded,
}

/** Whether a request only reads a machine's modpacks: the library, a pack's details and versions, a version's preview or an icon. */
export function isModpackRead(method: string, path: string): boolean {
  return (method === 'GET' || method === 'HEAD') && /^\/api\/machines\/\w+\/modpacks(\/.*)?$/.test(path)
}

/** The answer to a modpack read (see isModpackRead) on `world`, or undefined when nothing was recorded for it. */
export function answerModpackRead(url: URL, world: PackWorld): Answer | undefined {
  const m = /^\/api\/machines\/\w+\/modpacks(\/.*)?$/.exec(url.pathname)
  if (!m) return undefined
  const rest = m[1] ?? ''
  if (rest === '') return search(url.searchParams, world)
  if (rest === '/icon') return iconAnswer(url.searchParams.get('url') ?? '')
  const pack = /^\/([^/]+)\/([^/]+)(?:\/versions\/([^/]+)\/preview)?$/.exec(rest)
  if (!pack) return undefined
  const [source, project, version] = [pack[1], pack[2], pack[3]].map((s) => (s === undefined ? undefined : pathValue(s)))
  const problem = packRefProblem(source ?? '', project ?? '', version ?? '')
  if (problem) return invalid(problem)
  const src = source as Source
  if (src === 'curseforge' && !world.curseforge) return noCurseForge()
  if (src === 'curseforge' && !(Number(project) > 0 && /^\d+$/.test(project ?? ''))) return invalid("The pack's project is not valid.")
  const rec = recorded[src]
  const d = rec.details[project ?? ''] ?? Object.values(rec.details).find((x) => x.slug === project)
  if (!d) return undefined
  if (version === undefined) return json(200, structuredClone(d))
  const p = rec.previews[`${String(d.projectId)}/${version}`]
  return p ? json(200, structuredClone(p)) : undefined
}

/** The agent's check of the pack a request names (parsePackRef); version may be empty. */
function packRefProblem(source: string, project: string, version: string): string | undefined {
  const id = (s: string) => /^[A-Za-z0-9._-]{1,64}$/.test(s) && s.replace(/^\.+|\.+$/g, '') !== ''
  if (source !== 'modrinth' && source !== 'curseforge') return 'Modpacks come from Modrinth or CurseForge.'
  if (!id(project)) return 'That is not a valid modpack id.'
  if (version !== '' && !id(version)) return 'That is not a valid modpack version id.'
  return undefined
}

/** What the agent answers for CurseForge on a machine without a key (the library's noCurseForge). */
function noCurseForge(): Answer {
  return json(409, { error: 'CurseForge is not available on this Playkeeper: it has no CurseForge API key.', code: 'curseforge_unavailable', hint: 'Choose a pack from Modrinth. The owner can add a CurseForge API key to offer CurseForge packs.' })
}

/** The server types Playkeeper runs packs on, in the library's order. */
const packTypes = ['fabric', 'quilt', 'neoforge', 'vanilla']

/** One page of packs from one source (hModpackSearch, Library.Search). */
function search(q: URLSearchParams, world: PackWorld): Answer {
  const text = (q.get('q') ?? '').trim()
  if ([...text].length > 100) return invalid('Searches can be at most 100 characters.')
  if (!/^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u.test(text)) return invalid('Searches may not contain control characters.')
  const page = { offset: 0, limit: 0 }
  for (const name of ['offset', 'limit'] as const) {
    const raw = q.get(name) ?? ''
    if (raw && !/^[+-]?\d{1,18}$/.test(raw)) return invalid(`${name} must be a number.`)
    page[name] = Number(raw || 0)
  }
  const sort = q.get('sort') ?? ''
  if (!['', 'relevance', 'downloads', 'updated', 'newest'].includes(sort)) return invalid('Results can be sorted by relevance, downloads, last update or newest.')
  if (page.offset < 0 || page.offset > 10000 || page.limit < 0 || page.limit > 25) return invalid('That page of results is out of range.')
  const type = q.get('type') ?? ''
  if (type && !packTypes.includes(type)) return invalid('Playkeeper runs packs on Fabric, Quilt, NeoForge and Vanilla servers.')
  const version = q.get('version') ?? ''
  if (version && !(/^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$/.test(version) && compareMinecraft(version, '1.21') >= 0)) return invalid('Packs are offered for release versions of Minecraft from 1.21 on.')
  const limit = page.limit || 20
  const source = q.get('source') || 'modrinth'
  if (source === 'curseforge' && !world.curseforge) return noCurseForge()
  if (source !== 'modrinth' && source !== 'curseforge') return invalid(`Packs come from Modrinth or CurseForge, not "${source}".`)
  const rec = recorded[source]
  const listed = rec.searches[sort || 'relevance'] ?? { total: 0, cards: [] }
  let ids = listed.cards
  let total = listed.total
  if (text || type || version) {
    ids = [...new Set([listed.cards, ...Object.values(rec.searches).map((s) => s.cards)].flat())].filter((pid) => {
      const c = rec.cards[pid]
      return !!c && matches(c, text) && (!type || c.types.includes(type)) && (!version || c.minecraftVersions.includes(version))
    })
    if (sort === 'downloads') ids.sort((a, b) => (rec.cards[b]?.downloads ?? 0) - (rec.cards[a]?.downloads ?? 0))
    if (sort === 'updated') ids.sort((a, b) => Date.parse(rec.cards[b]?.updated ?? '') - Date.parse(rec.cards[a]?.updated ?? ''))
    total = ids.length
  }
  const cards = ids.slice(page.offset, page.offset + limit).map((pid) => structuredClone(rec.cards[pid]))
  return json(200, { cards, total, offset: page.offset, limit, sources: world.curseforge ? ['modrinth', 'curseforge'] : ['modrinth'] })
}

/** A stand-in for the sources' full-text search: every word is somewhere on the card. */
function matches(c: Card, text: string): boolean {
  const card = `${c.name} ${c.slug} ${c.author ?? ''} ${c.summary}`.toLowerCase()
  return text.toLowerCase().split(/\s+/).every((word) => card.includes(word))
}

/** Compares two release versions of Minecraft, part by part. */
function compareMinecraft(a: string, b: string): number {
  const pa = a.split('.').map(Number)
  const pb = b.split('.').map(Number)
  for (let i = 0; i < 3; i++) if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) - (pb[i] ?? 0)
  return 0
}

/** A path segment as Go's mux hands it over. */
function pathValue(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}
