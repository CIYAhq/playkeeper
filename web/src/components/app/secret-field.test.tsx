// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { SecretField } from './secret-field'

const styles = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../styles.css'), 'utf8')

let root: Root | undefined

/** The field, and a button that empties it as a save does. */
function Field() {
  const [value, setValue] = useState('')
  return (
    <>
      <SecretField id="key" value={value} onChange={setValue} label="OpenRouter key" placeholder="Paste your OpenRouter key" />
      <button type="button" id="saved" onClick={() => setValue('')} />
    </>
  )
}

async function render(masks: boolean) {
  vi.stubGlobal('CSS', { supports: (property: string, value: string) => masks && property === '-webkit-text-security' && value === 'disc' })
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<Field />))
}

const input = () => document.querySelector<HTMLInputElement>('input#key')!
const wrapper = () => document.querySelector<HTMLElement>('.secret-field')!
const toggle = () => document.querySelector<HTMLButtonElement>('button[aria-label="Show key"], button[aria-label="Hide key"]')

async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input(), value)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function press(el: HTMLElement | null) {
  if (!el) throw new Error('nothing to press')
  await act(async () => el.click())
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('SecretField', () => {
  it('is a text field that hides its letters, which no browser or password manager offers to save, fill or check', async () => {
    await render(true)
    const el = input()
    expect(el.type).toBe('text')
    expect(el.getAttribute('aria-label')).toBe('OpenRouter key')
    expect(el.placeholder).toBe('Paste your OpenRouter key')
    expect(el.getAttribute('autocomplete')).toBe('off')
    expect(el.getAttribute('autocapitalize')).toBe('none')
    expect(el.getAttribute('autocorrect')).toBe('off')
    expect(el.getAttribute('spellcheck')).toBe('false')
    for (const ignore of ['data-1p-ignore', 'data-lpignore', 'data-bwignore']) expect(el.hasAttribute(ignore), ignore).toBe(true)
    expect(el.getAttribute('data-form-type')).toBe('other')
    expect(wrapper().hasAttribute('data-shown')).toBe(false)
    expect(toggle(), 'nothing to show yet').toBeNull()
  })

  it('shows what’s typed only while asked, and hides the next key once the field empties', async () => {
    await render(true)
    await type('sk-or-v1-abcdefghijklmnop')
    expect(toggle()?.getAttribute('aria-label')).toBe('Show key')
    expect(wrapper().hasAttribute('data-shown')).toBe(false)

    await press(toggle())
    expect(wrapper().hasAttribute('data-shown')).toBe(true)
    expect(toggle()?.getAttribute('aria-label')).toBe('Hide key')
    await press(toggle())
    expect(wrapper().hasAttribute('data-shown')).toBe(false)

    await press(toggle())
    await press(document.querySelector<HTMLElement>('#saved'))
    expect(toggle()).toBeNull()
    expect(wrapper().hasAttribute('data-shown')).toBe(false)
    await type('sk-or-v1-qrstuvwxyzabcdef')
    expect(wrapper().hasAttribute('data-shown')).toBe(false)
    expect(toggle()?.getAttribute('aria-label')).toBe('Show key')
  })

  it('is a password field where the browser can’t hide a text field’s letters', async () => {
    await render(false)
    expect(input().type).toBe('password')
    await type('sk-or-v1-abcdefghijklmnop')
    await press(toggle())
    expect(input().type).toBe('text')
    await press(toggle())
    expect(input().type).toBe('password')
  })
})

describe('the secret field’s styles', () => {
  const css = styles.replace(/\/\*[\s\S]*?\*\//g, '')
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ selector: (m[1] ?? '').trim(), body: (m[2] ?? '').replace(/\s+/g, ' ').trim() }))
  const body = (selector: string) => rules.find((r) => r.selector === selector)?.body

  it('hide the letters until the field’s own button shows them, but never the placeholder', () => {
    expect(body('.secret-field input')).toBe('-webkit-text-security: disc;')
    expect(body('.secret-field[data-shown] input')).toBe('-webkit-text-security: none;')
    expect(body('.secret-field input::placeholder')).toBe('-webkit-text-security: none;')
  })

  it('hide each button a browser adds to such a field, in a rule of its own that browsers without it can’t drop with the others', () => {
    for (const part of ['-ms-reveal', '-ms-clear', '-webkit-caps-lock-indicator', '-webkit-credentials-auto-fill-button', '-webkit-contacts-auto-fill-button', '-webkit-strong-password-auto-fill-button']) {
      expect(body(`.secret-field input::${part}`), part).toMatch(/^display: none( !important)?;$/)
    }
    expect(rules.filter((r) => r.selector.includes('.secret-field') && r.selector.includes(',')), 'selector lists').toEqual([])
  })
})
