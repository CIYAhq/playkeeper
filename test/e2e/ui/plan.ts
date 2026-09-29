import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { apiReach } from './clickthrough-api.ts'
import { estimate, forPullRequest, freshSetup, importGraph, keysChanged, pageMapProblems, pullRequestShare, shardsFor, type Affected, type Costs, type Selection, type Shard, type Size } from './clickthrough-plan.ts'

// What CI's click-through crawls, and on how many runners:
//   node test/e2e/ui/plan.ts --full            every page (the release check)
//   node test/e2e/ui/plan.ts --base REV        what a pull request from REV to HEAD crawls: the pages it
//                                              touches, through their modules or the API (forPullRequest)
//   node test/e2e/ui/plan.ts --files a b ...   the same for changing those files
//   node test/e2e/ui/plan.ts --base REV --files a b ...
//                                              the pages those of the files the change from REV
//                                              changes touch (ci-since.sh's, for a push to a pull
//                                              request that already passed): the string table's
//                                              keys, and whether the played state is made fresh,
//                                              still come from the whole change
// With GITHUB_OUTPUT set it writes mode (none, pages or full), selection,
// views (the pages whose accessibility and width the runners check), matrix
// and setup (saved or fresh) for .github/workflows/clickthrough.yml, and a
// summary for the run.

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const args = process.argv.slice(2)
const flag = (name: string) => {
  const i = args.indexOf(name)
  return i >= 0 ? args[i + 1] : undefined
}

const graph = importGraph(path.join(root, 'web/src'))
const problems = pageMapProblems(graph)
if (problems.length) {
  console.error(`The click-through's page map (test/e2e/ui/clickthrough-plan.ts) is out of date:\n  ${problems.join('\n  ')}`)
  process.exit(1)
}

const costs = JSON.parse(fs.readFileSync(path.join(root, 'test/e2e/ui/clickthrough-costs.json'), 'utf8')) as Costs
const git = (...a: string[]) => execFileSync('git', a, { cwd: root, encoding: 'utf8', maxBuffer: 64 << 20 })

// The modules that name a key of the string table: as a string, or in a
// template the key is built from, as t(`style.world.${level}.desc`).
const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
const sources = [...graph.keys()]
  .filter((f) => f !== 'i18n/en.ts')
  .map((f) => {
    const text = fs.readFileSync(path.join(root, 'web/src', f), 'utf8')
    const templates = [...text.matchAll(/`([\w.-]+\$\{[^}`]*\}(?:[\w.-]|\$\{[^}`]*\})*)`/g)].map((m) => new RegExp(`^${(m[1] ?? '').split(/\$\{[^}`]*\}/).map(escape).join('.+')}$`))
    return { f, text, templates }
  })
const usesOf = (key: string) => {
  const names = [`'${key}'`, `"${key}"`, `\`${key}\``]
  return sources.filter(({ text, templates }) => names.some((n) => text.includes(n)) || templates.some((re) => re.test(key))).map(({ f }) => f)
}

/** The lines of each changed Go file the change touched, as the file is now: every line when there's no base to diff from. */
function goChanges(files: string[], base?: string, head = 'HEAD'): Map<string, number[] | undefined> {
  const go = files.filter((f) => /^internal\/.*\.go$/.test(f) && !f.endsWith('_test.go'))
  const out = new Map<string, number[] | undefined>(go.map((f) => [f, base ? [] : undefined]))
  if (!base || !go.length) return out
  let lines: number[] | undefined
  for (const line of git('diff', '-U0', '--no-renames', base, head, '--', ...go).split('\n')) {
    const file = /^\+\+\+ (?:b\/(.*)|\/dev\/null)$/.exec(line)
    if (file) lines = file[1] ? out.get(file[1]) : undefined
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/.exec(line)
    if (hunk && lines) {
      const from = Number(hunk[1])
      const count = hunk[2] === undefined ? 1 : Number(hunk[2])
      lines.push(...(count ? Array.from({ length: count }, (_, i) => from + i) : [from, from + 1]))
    }
  }
  return out
}

let plan: Affected
let whole: string[] = []
if (args.includes('--full')) {
  plan = { mode: 'full', pages: [], preludes: [], views: [], why: ['every page was asked for'] }
} else {
  const files = args.indexOf('--files')
  const base = flag('--base')
  const head = flag('--head') ?? 'HEAD'
  let changed: string[]
  let keys: string[] | undefined
  if (files < 0 && !base) {
    console.error('usage: plan.ts --full | --base REV [--head REV] [--files FILE...] | --files FILE...')
    process.exit(2)
  }
  if (!base) changed = whole = args.slice(files + 1)
  else {
    whole = git('diff', '--name-only', '--no-renames', base, head).split('\n').filter(Boolean)
    const only = files >= 0 ? new Set(args.slice(files + 1)) : undefined
    changed = only ? whole.filter((f) => only.has(f)) : whole
    if (changed.includes('web/src/i18n/en.ts')) {
      const text = (rev: string) => {
        try {
          return git('show', `${rev}:web/src/i18n/en.ts`)
        } catch {
          return ''
        }
      }
      keys = keysChanged(git('diff', '-U0', '--no-renames', base, head, '--', 'web/src/i18n/en.ts'), text(base), text(head))
    }
  }
  plan = forPullRequest(changed, graph, keys, usesOf, apiReach(root, graph, goChanges(changed, base, head)))
}
const selection: Selection = plan.mode === 'full' ? 'all' : { pages: plan.pages, preludes: plan.preludes }
let shards: Shard[] = plan.mode === 'none' ? [] : shardsFor(costs, selection, plan.mode === 'full' ? undefined : pullRequestShare(costs, selection))
// Pages only views.spec.ts opens still take a runner at each size, which crawls nothing.
if (plan.mode === 'pages' && !shards.length) shards = [{ size: 'desktop', shard: 1, of: 1 }, { size: 'phone', shard: 1, of: 1 }]
const fresh = freshSetup(whole)

const runners = (size: Size) => {
  const n = shards.filter((s) => s.size === size).length
  return n ? `${n} at ${size} size (${Math.round(estimate(costs, size, selection).seconds / 60)} min of pages)` : `none at ${size} size`
}
const crawled = plan.mode === 'pages' && plan.pages.length ? plan.pages : []
const lines = [
  plan.mode === 'none'
    ? 'Click-through: none, no page of the dashboard changed.'
    : plan.mode === 'full'
      ? 'Click-through: every page.'
      : crawled.length
        ? `Click-through: ${crawled.length} page${crawled.length === 1 ? '' : 's'} the change touches, after ${plan.preludes.join(' and ') || 'nothing'} as they are.`
        : 'Click-through: no page it crawls; its runners check the pages below.',
  ...crawled.map((p) => `  ${p}`),
  ...(plan.views.length ? [`Accessibility and width, desktop and narrow (views.spec.ts), split between the runners: ${plan.views.join(', ')}.`] : []),
  ...(shards.length ? [`Runners: ${runners('desktop')}, ${runners('phone')}.`] : []),
  ...(shards.length ? [fresh ? `Setup: the onboarding and the bots on every runner, since ${fresh}.` : 'Setup: the newest played state an ancestor of this commit saved, else the onboarding and the bots.'] : []),
  'Why:',
  ...plan.why.map((w) => `  ${w}`),
]
console.log(lines.join('\n'))

if (process.env.GITHUB_OUTPUT) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, [`mode=${plan.mode}`, `selection=${JSON.stringify(selection)}`, `views=${JSON.stringify(plan.views)}`, `matrix=${JSON.stringify({ include: shards })}`, `setup=${fresh ? 'fresh' : 'saved'}`, ''].join('\n'))
}
if (process.env.GITHUB_STEP_SUMMARY) {
  fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### ${lines[0]}\n\n\`\`\`\n${lines.slice(1).join('\n')}\n\`\`\`\n`)
}
