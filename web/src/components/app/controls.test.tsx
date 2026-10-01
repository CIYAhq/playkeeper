// @vitest-environment happy-dom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { ChoiceSelect } from './controls'

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

describe('ChoiceSelect', () => {
  // A list of choices that differ only in a number is told apart by its
  // name, by a screen reader and by the click-through alike.
  it('names its list of choices after its label on a desktop, as on a phone', async () => {
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    const options = [
      { value: '2048', label: '2 GB' },
      { value: '4096', label: '4 GB' },
    ]
    await act(async () => r.render(<ChoiceSelect value="4096" onChange={() => {}} options={options} label="Memory" />))
    const trigger = document.querySelector<HTMLElement>('[aria-label="Memory"]')
    await act(async () => trigger?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 })))
    const list = document.querySelector('[role="listbox"]')
    expect(list?.getAttribute('aria-label')).toBe('Memory')
    expect(list?.textContent).toContain('2 GB')
  })

  // On a phone the choices are in a sheet that goes as soon as one is made,
  // so the choice shows only on what opened it. As a combobox's value, a
  // screen reader reads it and the click-through compares it digits and all.
  it('holds its choice as a combobox on a phone too, where its list opens in a sheet', async () => {
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    try {
      const r = createRoot(document.body.appendChild(document.createElement('div')))
      root = r
      const options = ['cx23', 'cx33', 'cx43', 'cx53'].map((v) => ({ value: v, label: v.toUpperCase() }))
      function ServerType() {
        const [value, setValue] = useState('cx53')
        return <ChoiceSelect value={value} onChange={setValue} options={options} label="Server type" />
      }
      await act(async () => r.render(<ServerType />))
      const box = document.querySelector<HTMLElement>('[role="combobox"]')
      expect(box?.getAttribute('aria-label')).toBe('Server type')
      expect(box?.textContent).toBe('CX53')
      expect(box?.getAttribute('aria-expanded')).toBe('false')
      expect(box?.hasAttribute('aria-controls')).toBe(false)

      await act(async () => box?.click())
      expect(box?.getAttribute('aria-expanded')).toBe('true')
      const list = document.getElementById(box?.getAttribute('aria-controls') ?? '')
      expect(list?.getAttribute('role')).toBe('listbox')
      const cx33 = [...(list?.querySelectorAll<HTMLElement>('[role="option"]') ?? [])].find((o) => o.textContent === 'CX33')
      await act(async () => cx33?.click())
      expect(box?.textContent).toBe('CX33')
      expect(box?.getAttribute('aria-expanded')).toBe('false')
    } finally {
      phone.mockRestore()
    }
  })
})
