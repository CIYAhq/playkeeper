import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { json, type Answer } from './addon-fixtures'

// Recorded NeoForge and Forge version lists, so the click-through never waits
// on their Maven servers: NeoForge's answers 404 now and then for files it
// has, and a New server page that picked it then showed an error instead of
// its versions. fixtures/software/software.json holds what a panel built from
// the v0.4.2 release branch answered on 2026-09-27: each type's catalog
// versions, and the builds of every one of them. A catalog read still goes to
// the panel, for this machine's memory, servers and port, and gets the
// recorded versions laid over it; a builds read is answered here and never
// reaches the panel. A version nothing was recorded for gets no answer, and
// the harness reports it.

type Json = Record<string, unknown>

interface Recorded {
  catalog: { versions: Json[]; versionsCheckedAt: string }
  /** The builds answer of each recorded version, by Minecraft version. */
  builds: Record<string, Json>
}

const here = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures', 'software')
const recorded = JSON.parse(readFileSync(path.join(here, 'software.json'), 'utf8')) as Record<string, Recorded>

/** The server types whose versions and builds come from the recording. */
export const recordedTypes = Object.keys(recorded)

const catalogPath = /^\/api\/machines\/\w+\/catalog$/
const buildsPath = /^\/api\/machines\/\w+\/catalog\/builds$/

/** Whether a request reads a recorded type's builds, which only the recording answers. */
export function isRecordedBuildsRead(method: string, url: URL): boolean {
  return method === 'GET' && buildsPath.test(url.pathname) && recordedTypes.includes(url.searchParams.get('type') ?? '')
}

/** The recorded builds of the version a builds read asks for, or undefined when that version wasn't recorded. */
export function answerBuildsRead(url: URL): Answer | undefined {
  const builds = recorded[url.searchParams.get('type') ?? '']?.builds[url.searchParams.get('version') ?? '']
  return builds && json(200, builds)
}

/** A recorded type's catalog, as the panel answered it with the recorded versions laid over its own, or undefined for any other read. */
export function layRecordedCatalog(url: URL, body: unknown): Json | undefined {
  const rec = recorded[url.searchParams.get('type') ?? '']
  if (!rec || !catalogPath.test(url.pathname) || !body || typeof body !== 'object') return undefined
  const catalog: Json = { ...(body as Json), versions: rec.catalog.versions, versionsCheckedAt: rec.catalog.versionsCheckedAt }
  delete catalog.versionsError
  return catalog
}
