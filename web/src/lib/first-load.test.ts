import { describe, expect, it } from 'vitest'
import { budget, firstLoads, overBudget, type BuiltChunk } from './first-load'

const chunk = (fileName: string, bytes: number, over: Partial<BuiltChunk> = {}): BuiltChunk => ({ fileName, code: 'x'.repeat(bytes), imports: [], dynamicImports: [], isEntry: false, pages: [], ...over })

// The shape of the dashboard's build: an entry that loads the dashboard lazily,
// which loads each page lazily; a server's page loads its tabs lazily.
function build(loginBytes: number): BuiltChunk[] {
  return [
    chunk('index.js', 240_000, { isEntry: true, imports: ['react.js'], dynamicImports: ['App.js', 'pack.js'] }),
    chunk('react.js', 200_000),
    chunk('App.js', 110_000, { imports: ['react.js', 'ui.js'], dynamicImports: ['login.js', 'server.js'], pages: [] }),
    chunk('ui.js', 160_000),
    chunk('login.js', loginBytes, { imports: ['ui.js'], pages: ['pages/login.tsx'] }),
    chunk('server.js', 80_000, { imports: ['ui.js', 'chart.js'], dynamicImports: ['settings.js'], pages: ['pages/server/index.tsx'] }),
    chunk('chart.js', 20_000),
    chunk('settings.js', 60_000, { imports: ['chart.js'], pages: ['pages/server/settings.tsx'] }),
    chunk('pack.js', 30_000, { imports: ['react.js'], pages: ['pages/pack.tsx'] }),
  ]
}

describe('first loads', () => {
  it('counts the entry, every lazy chunk on the way to a page and what each imports, once each', () => {
    const loads = new Map(firstLoads(build(20_000)).map((l) => [l.page, l]))
    expect(loads.get('pages/login.tsx')).toMatchObject({ files: 5, bytes: 240_000 + 200_000 + 110_000 + 160_000 + 20_000 })
    expect(loads.get('pages/server/settings.tsx')).toMatchObject({ files: 7, bytes: 240_000 + 200_000 + 110_000 + 160_000 + 80_000 + 20_000 + 60_000 })
    expect(loads.get('pages/pack.tsx')).toMatchObject({ files: 3, bytes: 240_000 + 200_000 + 30_000 })
    expect(loads.get('pages/login.tsx')?.gzipBytes).toBeLessThan(loads.get('pages/login.tsx')?.bytes ?? 0)
    expect(overBudget([...loads.values()])).toEqual([])
  })

  it('fails a sign-in page over its budget, and any page over the most a page may load', () => {
    const heavy = budget.signIn.bytes - (240_000 + 200_000 + 110_000 + 160_000) + 1
    expect(overBudget(firstLoads(build(heavy))).join('\n')).toMatch(/^the sign-in page loads [\d,.]+ kB \([\d,.]+ kB gzipped\), over its 900\.0 kB/)
    const huge = firstLoads(build(20_000)).map((l) => (l.page === 'pages/server/settings.tsx' ? { ...l, bytes: budget.anyPage.bytes + 1 } : l))
    expect(overBudget(huge)).toEqual([expect.stringMatching(/^pages\/server\/settings\.tsx loads 1,250\.0 kB/)])
    expect(overBudget([])).toEqual(['no first load for pages/login.tsx'])
  })
})
