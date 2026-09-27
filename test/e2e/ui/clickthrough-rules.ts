import { failureList, type Result, type Status } from './crawl'
import { pageModules, pageOf, parseName, selects, where, type CrawlerName, type Selection, type Size } from './clickthrough-plan'
import type { View } from './fakes'

// What a click-through run must show, and the gate that checks it over the
// reports of every runner that crawled part of it (clickthrough.spec.ts
// writes them; clickthrough-gate.spec.ts, or a run on one runner itself,
// reads them).

/**
 * The fewest controls a page must have pressed, a little under what it has
 * now, so a page that stops showing its controls fails even when nothing on
 * it is broken. A page's count leaves out controls pressed on an earlier
 * page, such as the sidebar: the gate counts in the order of a run on one
 * runner, however the pages were split. A page that isn't listed needs
 * one. A dev build (make dev) can't update itself, so its /settings has no
 * "Check for updates" and one control fewer than an installed panel's. The
 * add-on library shows the recorded fixtures' cards (addon-fixtures.ts), so
 * its count doesn't move with what Modrinth and Hangar list. A fresh install
 * has nothing to free on the Disk space page, whose way back to the machine
 * is pressed on the machine's other pages first, so its controls count in
 * the space to free view.
 */
export const minimums: Record<Size, Record<string, number>> = {
  desktop: {
    '/login': 3,
    '/setup (first run)': 1,
    '/': 26,
    '/servers/*': 21,
    '/servers/*/console': 12,
    '/servers/*/players': 9,
    '/servers/*/world': 14,
    '/servers/*/plugins': 1,
    '/servers/*/settings': 36,
    '/servers/new': 36,
    '/servers/*/plugins/browse': 107,
    '/machines/*': 3,
    '/machines/*/settings': 6,
    '/machines/*/disk': 0,
    '/settings': 2,
    '/account': 7,
    '/account/two-factor': 7,
    '/servers/*/world/pregen': 5,
    '/login (second step)': 8,
    '/servers/*/plugins (in use)': 40,
    '/servers/*/world (in use)': 2,
    '/servers/*/world/packs (in use)': 6,
    '/servers/*/world/pregen (in use)': 2,
    '/servers/*/world/pregen (paused)': 1,
    '/servers/*/players (friends and team)': 5,
    '/settings/team (friends and team)': 10,
    '/settings/discord (friends and team)': 5,
    '/servers/*/map (map on)': 12,
    '/ (stopped)': 3,
    '/servers/* (stopped)': 1,
    '/servers/*/console (stopped)': 3,
    '/servers/*/settings (stopped)': 1,
    '/ (crashed)': 1,
    '/servers/* (crashed)': 3,
    '/servers/* (busy)': 2,
    '/servers/*/world (busy)': 1,
    '/servers/*/players (empty lists)': 1,
    '/servers/*/world (empty lists)': 1,
    '/ (no servers)': 1,
    '/welcome (no servers)': 11,
    '/settings (update available)': 5,
    '/machines/*/disk (space to free)': 11,
    '/servers/*/file/server.properties': 5,
    '/servers/*/files (a few files)': 72,
    '/servers/*/files/plugins (a few files)': 33,
  },
  phone: {
    '/login': 3,
    '/setup (first run)': 1,
    '/': 5,
    '/servers/*': 26,
    '/servers/*/console': 9,
    '/servers/*/players': 5,
    '/servers/*/world': 9,
    '/servers/*/plugins': 1,
    '/servers/*/settings': 34,
    '/servers/new': 29,
    '/servers/*/plugins/browse': 81,
    '/machines/*': 1,
    '/machines/*/settings': 6,
    '/machines/*/disk': 0,
    '/settings': 1,
    '/account': 6,
    '/account/two-factor': 8,
    '/more': 5,
    '/servers/*/world/pregen': 4,
    '/login (second step)': 8,
    '/servers/*/plugins (in use)': 20,
    '/servers/*/world/packs (in use)': 6,
    '/servers/*/world/pregen (in use)': 2,
    '/servers/*/world/pregen (paused)': 1,
    '/servers/*/players (friends and team)': 5,
    '/settings/team (friends and team)': 10,
    '/settings/discord (friends and team)': 5,
    '/servers/*/map (map on)': 12,
    '/ (stopped)': 1,
    '/servers/* (stopped)': 2,
    '/servers/*/console (stopped)': 3,
    '/servers/*/settings (stopped)': 1,
    '/ (crashed)': 1,
    '/servers/* (crashed)': 3,
    '/servers/*/world (busy)': 1,
    '/servers/*/players (empty lists)': 1,
    '/servers/*/world (empty lists)': 1,
    '/ (no servers)': 1,
    '/welcome (no servers)': 15,
    '/settings (update available)': 3,
    '/more (update available)': 1,
    '/machines/*/disk (space to free)': 14,
    '/servers/*/file/server.properties': 4,
    '/servers/*/files (a few files)': 115,
    '/servers/*/files/plugins (a few files)': 52,
  },
}

/**
 * What a page needs instead when the build carries CurseForge's key, as the
 * Release check's does, since it's the release: Settings › Add-on sources
 * then says the key is built in, with nothing to press, so on a phone, where
 * it's a page of its own, it has no controls of its own.
 */
export const keyMinimums: Record<Size, Record<string, number>> = { desktop: {}, phone: { '/settings/addon-sources': 0 } }

export interface Place {
  /** What it is, for the report. */
  what: string
  sizes: Size[]
  view?: View
  /** The page it's on, as the minimums name pages: a run of some pages must reach it when it crawls that page. */
  page: string
  /** The page it's on at phone size, when that's another. */
  phonePage?: string
  /** The control that must be pressed there. */
  key: RegExp
  /** How it must come out; the default is that it works. */
  status?: Status
}

/**
 * Places the click-through didn't reach before 0.3.1's audit, the Disk
 * space page's clean-up, and a select whose choices differ only in a number.
 * In each, one control must come out as `status`, and a negative control
 * breaks it and presses it again (a disabled one loses its reason instead):
 * the crawl must then report it.
 */
export const places: Place[] = [
  { what: '"Restore this backup?", a dialog that replaces the menu or sheet it opens from', sizes: ['desktop', 'phone'], page: '/servers/*/world', key: /^button "Cancel" in dialog "Restore this backup\?"$/ },
  { what: 'the typed confirmation in "Restore this backup?"', sizes: ['desktop', 'phone'], page: '/servers/*/world', key: /^button "Replace the world and restore" in dialog "Restore this backup\?"$/ },
  { what: '"Delete this backup?", which replaces its menu', sizes: ['desktop'], page: '/servers/*/world', key: /^button "Delete backup" in dialog "Delete this backup\?"$/ },
  { what: 'the phone’s "Update to" sheet, which replaces its dialog', sizes: ['phone'], page: '/servers/*/settings', key: /^option ".+" in listbox "Update to"$/ },
  { what: 'the view distance slider', sizes: ['desktop'], page: '/servers/*/settings', key: /^slider "View distance"/ },
  { what: 'the memory slider in New server', sizes: ['desktop', 'phone'], page: '/servers/new', key: /^slider "Memory for this server"/ },
  { what: 'the last step of New server', sizes: ['desktop', 'phone'], page: '/servers/new', key: /^button "Create and start / },
  { what: 'Start on a stopped server', sizes: ['desktop', 'phone'], view: 'stopped', page: '/', key: /^button "Start"$/ },
  // On a phone the fix's button is pinned above the tabs, outside "How to fix it".
  { what: 'the fix on a crashed server', sizes: ['desktop', 'phone'], view: 'crashed', page: '/servers/*', key: /^button "Save and start .+"( in "How to fix it")?$/ },
  { what: 'Restart while a backup runs', sizes: ['desktop'], view: 'busy', page: '/servers/*', key: /^button "Restart" \[disabled\]$/, status: 'disabled with a reason' },
  { what: 'the empty Players page', sizes: ['desktop', 'phone'], view: 'empty lists', page: '/servers/*/players', key: /^button "Add player"$/ },
  { what: 'the empty World page', sizes: ['desktop', 'phone'], view: 'empty lists', page: '/servers/*/world', key: /^button "Make my first backup"$/ },
  { what: 'Home with no servers', sizes: ['desktop', 'phone'], view: 'no servers', page: '/', key: /^link "(Next: )?Create your first server"$/ },
  { what: 'the end of onboarding (/welcome)', sizes: ['desktop', 'phone'], view: 'no servers', page: '/welcome', key: /^button "Create my server"$/ },
  { what: 'a memory choice in onboarding’s "Change the details", which changes only a number', sizes: ['desktop'], view: 'no servers', page: '/welcome', key: /^option "# GB" in listbox ""( #\d+)?$/ },
  { what: 'installing a Playkeeper update', sizes: ['desktop', 'phone'], view: 'update available', page: '/settings', key: /^button "Update( now)?" in dialog "Update Playkeeper to .+"$/ },
  { what: 'waking a sleeping server', sizes: ['desktop', 'phone'], view: 'asleep', page: '/servers/*', key: /^button "Wake up now" in ".+ is asleep"$/ },
  { what: 'a new recovery key for the copies somewhere else', sizes: ['desktop', 'phone'], view: 'looks after itself', page: '/servers/*/world/backup-rules', phonePage: '/servers/*/world/backup-rules/copies', key: /^button "Download new key" in dialog "New recovery key made"$/ },
  { what: 'pausing a schedule', sizes: ['phone'], view: 'looks after itself', page: '/servers/*/settings/schedules', key: /^switch "Run “Restart every day at #:#”" in row "Restart every day at #:#"$/ },
  // A phone's review sheet has the design's shorter title.
  { what: 'deleting old backups on the Disk space page', sizes: ['desktop'], view: 'space to free', page: '/machines/*/disk', key: /^button "Delete # · .+" in dialog "Backups beyond your keep rules"$/ },
  { what: 'deleting old backups on the Disk space page', sizes: ['phone'], view: 'space to free', page: '/machines/*/disk', key: /^button "Delete # · .+" in dialog "Old backups"$/ },
  { what: 'first-run setup', sizes: ['desktop', 'phone'], view: 'first run', page: '/setup', key: /^button "Create account and continue"$/ },
  { what: 'two-factor sign-in’s second step', sizes: ['desktop', 'phone'], view: 'second step', page: '/login', key: /^button "Sign in" in "Enter your code"$/ },
  { what: 'finishing two-factor setup', sizes: ['desktop', 'phone'], page: '/account/two-factor', key: /^button "I’ve saved them"/ },
  { what: 'updating every plugin at once', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/plugins', key: /^button "Update all"/ },
  { what: 'managing a plugin added by hand', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/plugins', key: /^button "Let Playkeeper manage it"/ },
  { what: 'forgetting a plugin whose file is gone', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/plugins', key: /^button "Forget"/ },
  { what: 'a data pack’s switch', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/world/packs', key: /^switch "more-mob-heads"/ },
  { what: 'the resource pack’s "must accept" switch', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/world/packs', key: /^switch "(Players must accept it to join|Must accept to join)"/ },
  { what: 'pausing pre-generation', sizes: ['desktop', 'phone'], view: 'in use', page: '/servers/*/world/pregen', key: /^button "Pause"/ },
  { what: 'resuming pre-generation', sizes: ['desktop', 'phone'], view: 'paused', page: '/servers/*/world/pregen', key: /^button "Resume"/ },
  { what: 'starting pre-generation (the phone’s action bar)', sizes: ['phone'], page: '/servers/*/world/pregen', key: /^button "Start" in group "Pre-generate"$/ },
  { what: 'letting a friend in from a join request', sizes: ['desktop', 'phone'], view: 'friends and team', page: '/servers/*/players', key: /^button "Let in"$/ },
  { what: 'turning off a friend link', sizes: ['desktop', 'phone'], view: 'friends and team', page: '/servers/*/players', key: /^menuitem "Turn off" in menu ""$/ },
  { what: 'turning off an unused team invite', sizes: ['desktop', 'phone'], view: 'friends and team', page: '/settings/team', key: /^(menuitem|button) "Turn off link"/ },
  { what: 'saving a team member’s role and servers', sizes: ['desktop', 'phone'], view: 'friends and team', page: '/settings/team', key: /^button "Save" in dialog "alex’s role and servers"/ },
  { what: 'Discord’s test message', sizes: ['desktop', 'phone'], view: 'friends and team', page: '/settings/discord', key: /^button "Send test message"/ },
  { what: 'sharing the map with a link', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^switch "Share with a link"/ },
  { what: 'another world on the map', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^button "Nether" in group "Worlds"$/ },
  { what: 'a player’s marker on the map', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^button "Show Pixel_Pia on the map"$/ },
  { what: 'the restart that starts the map', sizes: ['desktop', 'phone'], view: 'map restart', page: '/servers/*/map', key: /^button "Restart now" in "One restart/ },
  // The map's area, while 2,500 blocks are being filled in: its place in the map's options, a bigger area with its pause switch and Start, and Explored only's Stop.
  { what: 'the map’s area in its options', sizes: ['desktop'], view: 'map on', page: '/servers/*/map', key: /^menuitem "Map area…" in menu ""$/ },
  { what: 'the map’s area in the phone’s Map settings', sizes: ['phone'], view: 'map on', page: '/servers/*/map', key: /^button "Map area .+" in dialog "Map settings"$/ },
  { what: 'a bigger map area', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^radio "#,# blocks about .+" in dialog "Map area" > radiogroup "Map area"$/ },
  { what: 'pausing a map area while people play', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^switch "Pause while people (are playing|play)" in dialog "Map area"$/ },
  { what: 'filling in a bigger map area', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^button "Start" in dialog "Map area"$/ },
  { what: 'stopping the map area being filled in', sizes: ['desktop', 'phone'], view: 'map on', page: '/servers/*/map', key: /^button "Stop" in dialog "Map area"$/ },
  { what: 'deleting in the Files tab, after asking', sizes: ['desktop', 'phone'], view: 'a few files', page: '/servers/*/files', key: /^button "Delete" in dialog "Delete .+\?"$/ },
  { what: 'making a folder in the Files tab', sizes: ['desktop', 'phone'], view: 'a few files', page: '/servers/*/files', key: /^button "Create folder" in dialog "New folder"/ },
  { what: 'uploading into a folder of the Files tab', sizes: ['desktop'], view: 'a few files', page: '/servers/*/files', key: /^button "Upload" in "Files"$/ },
  { what: 'uploading from the phone’s bar in the Files tab', sizes: ['phone'], view: 'a few files', page: '/servers/*/files', key: /^button "Upload files"$/ },
  { what: 'deleting a selection with the world in it while the game runs', sizes: ['desktop'], view: 'a few files', page: '/servers/*/files', key: /^button "Delete" in "Files" \[disabled\]$/, status: 'disabled with a reason' },
  { what: 'the world folder’s Delete while the game runs, in the phone’s sheet', sizes: ['phone'], view: 'a few files', page: '/servers/*/files', key: /^button "Delete" in dialog "world"( > row "Delete")? \[disabled\]$/, status: 'disabled with a reason' },
  { what: 'the editor’s Save before anything changed', sizes: ['desktop', 'phone'], page: '/servers/*/file/server.properties', key: /^button "Save" \[disabled\]$/, status: 'disabled with a reason' },
]

export interface Rules {
  minimums: Record<string, number>
  /** What a build that carries CurseForge's key needs instead (keyMinimums). */
  keyMinimums?: Record<string, number>
  places: Place[]
}

/** The page a place is on at a size. */
export function placePage(place: Place, size: Size): string {
  return (size === 'phone' && place.phonePage) || place.page
}

/**
 * The place's control, pressed on its page. Its negative control only means
 * something there: on another page the same control may seem to work with
 * its press stopped (on a stopped server's Console, the log read that
 * follows counts as the press loading something).
 */
export function found(results: Result[], place: Place, size: Size): Result | undefined {
  return results.find((r) => (r.view ?? 'live') === (place.view ?? 'live') && pageOf(r.route) === placePage(place, size) && place.key.test(r.key) && r.status === (place.status ?? 'works'))
}

export interface Negative {
  place: string
  /** Where the broken control was, and its key. */
  key: string
  /** What the crawl said about it once broken. */
  verdict: string
  caught: boolean
}

/** A control's result, with the page (unit) of the run it was pressed on. */
export type Pressed = Result & { unit: string }

/** What one runner reports: the run's pages and which runner crawls each, and what it found on its own. */
export interface ShardReport {
  size: Size
  shard: number
  of: number
  selection: Selection
  /** Every page the run crawls at this size, in order, and the runner that crawls it. */
  plan: { name: string; crawler: CrawlerName; counted: boolean; shard: number }[]
  /** This runner's pages as it crawled them, with the seconds each took, its negative controls included. */
  crawled: { name: string; seconds: number }[]
  results: Pressed[]
  notes: string[]
  /** States the crawl found but could not get back to, so their controls went unpressed. */
  unreached: string[]
  negatives: Negative[]
  /** Add-on and modpack reads that reached the panel or had no recorded answer. */
  fixtureProblems: string[]
  /** Where the machine's CurseForge key comes from, as Settings › Add-on sources reads it: "build" when the build carries one. */
  curseforge?: string
}

/** What fails a runner on its own: failing controls, states it couldn't get back to, reads that reached the panel, negative controls it didn't catch. */
export function ownProblems(r: ShardReport): string[] {
  const failures = failureList(r.results)
  return [...(failures ? [failures] : []), ...r.unreached, ...r.fixtureProblems, ...r.negatives.filter((n) => !n.caught).map((n) => `${r.size}: with ${n.place} broken, the crawl said "${n.verdict}" (${n.key})`)]
}

export interface Verdict {
  problems: string[]
  /** Each crawled page's controls, counted in the run's order: what the minimums hold. */
  counts: Map<string, number>
  results: Pressed[]
  negatives: Negative[]
  /** Each page's seconds, for clickthrough-costs.json. */
  costs: Record<string, number>
}

/**
 * Everything that fails a run at one size, over its runners' reports: a
 * runner that didn't report, runners that worked out different pages, a page
 * no runner crawled, failing controls, states a runner couldn't get back to,
 * pages under their minimum, places it didn't reach and negative controls
 * the crawl didn't catch. Every control of a page is pressed by the runner
 * that crawls it, there or on a page it crawled before (crawl.ts), so a run
 * that passes pressed every control of every page it plans.
 */
export function gate(size: Size, reports: ShardReport[], of: number, rules: Rules = { minimums: minimums[size], keyMinimums: keyMinimums[size], places }): Verdict {
  const problems: string[] = []
  const byShard = new Map<number, ShardReport>()
  for (const r of reports) {
    if (r.size !== size) continue
    if (r.of !== of) problems.push(`${size}: a report from runner ${r.shard} of ${r.of}, in a run of ${of}`)
    else if (byShard.has(r.shard)) problems.push(`${size}: two reports from runner ${r.shard} of ${of}`)
    else byShard.set(r.shard, r)
  }
  for (let k = 1; k <= of; k++) if (!byShard.has(k)) problems.push(`${size}: runner ${k} of ${of} sent no report, so its pages weren't all crawled`)
  const shards = [...byShard.values()].sort((a, b) => a.shard - b.shard)
  const first = shards[0]
  if (!first) return { problems, counts: new Map(), results: [], negatives: [], costs: {} }
  const plan = first.plan
  const planned = JSON.stringify(plan)
  for (const r of shards.slice(1)) {
    if (JSON.stringify(r.plan) === planned) continue
    const mine = new Set(r.plan.map((p) => `${p.name} → ${p.shard}`))
    const theirs = new Set(plan.map((p) => `${p.name} → ${p.shard}`))
    const diff = [...[...mine].filter((x) => !theirs.has(x)).map((x) => `+${x}`), ...[...theirs].filter((x) => !mine.has(x)).map((x) => `-${x}`)]
    problems.push(`${size}: runner ${r.shard} worked out other pages than runner ${first.shard} (${diff.slice(0, 6).join('; ') || 'in another order'}), so their pages may overlap or leave some out`)
  }
  for (const r of shards) {
    const want = plan.filter((p) => p.shard === r.shard).map((p) => p.name)
    const got = r.crawled.map((c) => c.name)
    for (const n of want) if (!got.includes(n)) problems.push(`${size}: runner ${r.shard} of ${of} didn't crawl ${n}`)
    for (const n of got) if (!want.includes(n)) problems.push(`${size}: runner ${r.shard} of ${of} crawled ${n}, which is another runner's page`)
  }

  const results = shards.flatMap((r) => r.results)
  for (const r of shards) problems.push(...ownProblems(r))

  // Each crawler counts a control once, on the first page it presses it, so
  // count every runner's controls in the order of a run on one runner.
  const counts = new Map<string, number>()
  const pageName = (unit: string) => unit.replace(/ #\d+$/, '')
  for (const p of plan) if (p.counted) counts.set(pageName(p.name), counts.get(pageName(p.name)) ?? 0)
  const pressed = new Map<CrawlerName, Set<string>>()
  for (const p of plan) {
    const keys = pressed.get(p.crawler) ?? new Set<string>()
    pressed.set(p.crawler, keys)
    for (const r of byShard.get(p.shard)?.results ?? []) {
      if (r.unit !== p.name || keys.has(r.key)) continue
      keys.add(r.key)
      counts.set(pageName(p.name), (counts.get(pageName(p.name)) ?? 0) + 1)
    }
  }
  const keyBuiltIn = shards.some((r) => r.curseforge === 'build')
  for (const [page, n] of counts) {
    const min = (keyBuiltIn ? rules.keyMinimums?.[page] : undefined) ?? rules.minimums[page] ?? 1
    if (n < min) problems.push(`${size} ${page}: ${n} control${n === 1 ? '' : 's'} pressed, fewer than its minimum of ${min}`)
  }
  const selection = first.selection
  for (const [page, min] of Object.entries(rules.minimums)) {
    const { page: route, view } = parseName(page)
    if (!counts.has(page) && selects(selection, { route, view })) problems.push(`${size} ${page}: not crawled (its minimum is ${min})`)
  }

  const negatives = shards.flatMap((r) => r.negatives)
  for (const place of rules.places) {
    if (!place.sizes.includes(size)) continue
    const page = placePage(place, size)
    const hit = found(results, place, size)
    if (!hit && selects(selection, { route: page, view: place.view ?? 'live' })) {
      const elsewhere = results.find((r) => (r.view ?? 'live') === (place.view ?? 'live') && place.key.test(r.key) && r.status === (place.status ?? 'works'))
      problems.push(`${size}: never pressed ${place.what} on ${where(page, place.view)} (${place.key})${elsewhere ? `, only on ${where(pageOf(elsewhere.route), elsewhere.view)}: if it moved, change its page in clickthrough-rules.ts` : ''}`)
    }
    if (hit && !negatives.some((n) => n.place === place.what)) problems.push(`${size}: pressed ${place.what} but didn't break it on purpose to check the crawl notices`)
  }

  if (selection === 'all') {
    for (const p of plan) {
      const { page } = parseName(p.name)
      if (p.counted && !pageModules[page]) problems.push(`${size} ${page}: not in pageModules (clickthrough-plan.ts); name the modules that draw it, so a change to them crawls it`)
    }
  }
  const costs = Object.fromEntries(shards.flatMap((r) => r.crawled.map((c) => [c.name, Math.round(c.seconds)] as const)))
  return { problems: [...new Set(problems)], counts, results, negatives, costs }
}

/** A readable name for where a result was pressed. */
export function at(r: Pick<Result, 'route' | 'view' | 'via' | 'key'>): string {
  return `${where(r.route, r.view)}${r.via.length ? ` › ${r.via.join(' › ')}` : ''} › ${r.key}`
}
