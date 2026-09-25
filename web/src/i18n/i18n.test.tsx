import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { en } from './en'
import { interpolate, message, t, type Plural } from '.'
import { rich } from './rich'

describe('t', () => {
  it('fills placeholders and formats numbers', () => {
    expect(t('op.backup', { server: 'Survival' })).toBe('Backing up Survival')
    expect(t('checklist.progress', { done: 2, total: 4 })).toBe('2 of 4')
    expect(interpolate('{count} blocks', { count: 12345 })).toBe('12,345 blocks')
  })

  it('writes ports and build numbers without digit grouping', () => {
    expect(t('onboarding.check.port', { port: 25565 })).toBe('Port 25565 is free')
    expect(t('new.build', { build: 1024 })).toBe('Paper build 1024')
  })

  it('leaves a placeholder without a value as it is', () => {
    expect(interpolate('Hello {name}', {})).toBe('Hello {name}')
  })

  it('picks plural forms, and the zero form for exactly none', () => {
    expect(t('unit.players', { count: 1 })).toBe('1 player')
    expect(t('unit.players', { count: 3 })).toBe('3 players')
    expect(t('home.playing', { count: 0 })).toBe('nobody playing')
    expect(t('home.playing', { count: 1 })).toBe('1 playing')
    // A message without a zero form uses the language's rule for 0.
    expect(t('unit.players', { count: 0 })).toBe('0 players')
  })

  it('renders tagged parts with the given components', () => {
    const html = renderToStaticMarkup(<>{rich('settings.deleteType', { b: (c) => <b>{c}</b> }, { server: 'Survival' })}</>)
    expect(html).toBe('Type <b>Survival</b> to confirm.')
    const code = renderToStaticMarkup(<>{rich('login.forgot', { code: (c) => <code>{c}</code> }, { command: 'sudo playkeeper reset-password <username>' })}</>)
    expect(code).toContain('<code>sudo playkeeper reset-password &lt;username&gt;</code>')
  })
})

describe('the English catalog', () => {
  const entries = Object.entries(en) as [string, string | Plural][]

  it('has text for every key and form', () => {
    for (const [key, value] of entries) {
      const forms = typeof value === 'string' ? [value] : Object.values(value)
      for (const f of forms) expect(f.trim(), key).not.toBe('')
      if (typeof value !== 'string') expect(value.other, key).toBeTruthy()
    }
  })

  it('uses the same placeholders in every plural form', () => {
    const names = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).filter((n) => n !== 'count').sort()
    for (const [key, value] of entries) {
      if (typeof value === 'string') continue
      const want = names(value.other)
      for (const f of Object.values(value)) expect(names(f), key).toEqual(want)
    }
  })

  it('balances the tags that rich text uses', () => {
    for (const [key, value] of entries) {
      const forms = typeof value === 'string' ? [value] : Object.values(value)
      for (const f of forms) {
        const open = [...f.matchAll(/<(\w+)>/g)].map((m) => m[1])
        const close = [...f.matchAll(/<\/(\w+)>/g)].map((m) => m[1])
        expect(close, key).toEqual(open)
      }
    }
  })

  it('has no keys that nothing uses', () => {
    const code = Object.values(import.meta.glob(['../**/*.{ts,tsx}', '!../**/*.test.*', '!./en.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>).join('\n')
    const used = new Set([...code.matchAll(/['"]([a-zA-Z]+\.[\w.-]+)['"]/g)].map((m) => m[1]))
    // Keys built from a value, like `settings.difficulty.${d}`.
    const families = ['settings.difficulty.', 'settings.mode.', 'style.world.', 'onboarding.strength.', 'overview.chartTitle.']
    const unused = entries.map(([k]) => k).filter((k) => !used.has(k) && !families.some((f) => k.startsWith(f)))
    expect(unused).toEqual([])
  })

  it('falls back to English for a missing key', () => {
    expect(message('brand.name')).toBe('Playkeeper')
  })
})
