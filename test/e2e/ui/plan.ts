import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { estimate, forPullRequest, freshSetup, importGraph, keysChanged, pageMapProblems, pullRequestShardSeconds, shardsFor, type Affected, type Costs, type Selection, type Size } from './clickthrough-plan.ts'

// What CI's click-through crawls, and on how many runners:
//   node test/e2e/ui/plan.ts --full            every page (the release check)
//   node test/e2e/ui/plan.ts --base REV        what a pull request from REV to HEAD crawls: the pages it
//                                              changes, never every page (forPullRequest)
//   node test/e2e/ui/plan.ts --files a b ...   the same for changing those files
// With GITHUB_OUTPUT set it writes mode (none, pages or full), selection,
// matrix and setup (saved or fresh) for .github/workflows/clickthrough.yml,
// and a summary for the run.

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

// The modules that name a key of the string table: as a string, or as the
// start of a template the key is built from, as t(`settings.mode.${mode}`).
const sources = [...graph.keys()].filter((f) => f !== 'i18n/en.ts').map((f) => ({ f, text: fs.readFileSync(path.join(root, 'web/src', f), 'utf8') }))
const usesOf = (key: string) => {
  const names = [`'${key}'`, `"${key}"`, `\`${key}\``]
  const parent = key.includes('.') ? `\`${key.slice(0, key.lastIndexOf('.'))}.\${` : undefined
  return sources.filter(({ text }) => names.some((n) => text.includes(n)) || (parent !== undefined && text.includes(parent))).map(({ f }) => f)
}

let plan: Affected
let changed: string[] = []
if (args.includes('--full')) {
  plan = { mode: 'full', pages: [], preludes: [], why: ['every page was asked for'] }
} else {
  const files = args.indexOf('--files')
  let keys: string[] | undefined
  if (files >= 0) changed = args.slice(files + 1)
  else {
    const base = flag('--base')
    if (!base) {
      console.error('usage: plan.ts --full | --base REV [--head REV] | --files FILE...')
      process.exit(2)
    }
    const head = flag('--head') ?? 'HEAD'
    changed = git('diff', '--name-only', '--no-renames', base, head).split('\n').filter(Boolean)
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
  plan = forPullRequest(changed, graph, costs, keys, usesOf)
}
const selection: Selection = plan.mode === 'full' ? 'all' : { pages: plan.pages, preludes: plan.preludes }
const shards = plan.mode === 'none' ? [] : shardsFor(costs, selection, plan.mode === 'full' ? undefined : pullRequestShardSeconds)
const fresh = freshSetup(changed)

const runners = (size: Size) => {
  const n = shards.filter((s) => s.size === size).length
  return n ? `${n} at ${size} size (${Math.round(estimate(costs, size, selection).seconds / 60)} min of pages)` : `none at ${size} size`
}
const lines = [
  plan.mode === 'none' ? 'Click-through: none, no page of the dashboard changed.' : plan.mode === 'full' ? 'Click-through: every page.' : `Click-through: ${plan.pages.length} page${plan.pages.length === 1 ? '' : 's'} the change touches, after ${plan.preludes.join(' and ') || 'nothing'} as they are.`,
  ...(plan.mode === 'pages' ? plan.pages.map((p) => `  ${p}`) : []),
  ...(shards.length ? [`Runners: ${runners('desktop')}, ${runners('phone')}.`] : []),
  ...(shards.length ? [fresh ? `Setup: the onboarding and the bots on every runner, since ${fresh}.` : 'Setup: the newest played state an ancestor of this commit saved, else the onboarding and the bots.'] : []),
  'Why:',
  ...plan.why.map((w) => `  ${w}`),
]
console.log(lines.join('\n'))

if (process.env.GITHUB_OUTPUT) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, [`mode=${plan.mode}`, `selection=${JSON.stringify(selection)}`, `matrix=${JSON.stringify({ include: shards })}`, `setup=${fresh ? 'fresh' : 'saved'}`, ''].join('\n'))
}
if (process.env.GITHUB_STEP_SUMMARY) {
  fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### ${lines[0]}\n\n\`\`\`\n${lines.slice(1).join('\n')}\n\`\`\`\n`)
}
