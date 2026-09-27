import { expect, test } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { sizeNames, type Costs, type Shard } from './clickthrough-plan'
import { gate, type ShardReport } from './clickthrough-rules'
import { outDir } from './helpers'

// The gate of a click-through split between runners: it reads every
// runner's report from PK_REPORTS and, for each size in PK_MATRIX (the
// matrix plan.ts made), checks that together they crawled every page of the
// run and pressed every page's controls (clickthrough-rules.ts). It writes
// the merged results, and each page's seconds for clickthrough-costs.json,
// to PK_OUT.

test('the runners together pressed every control of every page', () => {
  const dir = process.env.PK_REPORTS ?? ''
  expect(dir, 'PK_REPORTS').not.toBe('')
  const matrix = JSON.parse(process.env.PK_MATRIX ?? '{"include":[]}') as { include: Shard[] }
  const reports = fs
    .readdirSync(dir, { recursive: true, encoding: 'utf8' })
    .filter((f) => /(^|\/)clickthrough-(desktop|phone)(-\d+of\d+)?\.json$/.test(f))
    .map((f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')) as ShardReport)
  const problems: string[] = []
  const costs: Costs = {}
  const merged: Record<string, unknown> = {}
  for (const size of sizeNames) {
    const of = matrix.include.find((s) => s.size === size)?.of
    if (!of) continue
    const verdict = gate(size, reports, of)
    problems.push(...verdict.problems)
    costs[size] = verdict.costs
    merged[size] = { counts: Object.fromEntries(verdict.counts), results: verdict.results, negatives: verdict.negatives }
    const minutes = reports.filter((r) => r.size === size).map((r) => `${r.shard}: ${Math.round(r.crawled.reduce((s, c) => s + c.seconds, 0) / 60)} min`)
    console.log(`${size}: ${verdict.results.length} controls on ${verdict.counts.size} pages from ${of} runner${of === 1 ? '' : 's'} (${minutes.join(', ')})`)
  }
  fs.mkdirSync(outDir, { recursive: true })
  fs.writeFileSync(path.join(outDir, 'clickthrough-merged.json'), JSON.stringify(merged, null, 2))
  fs.writeFileSync(path.join(outDir, 'clickthrough-costs.json'), `${JSON.stringify(costs, null, 2)}\n`)
  expect(problems, problems.join('\n')).toEqual([])
})
