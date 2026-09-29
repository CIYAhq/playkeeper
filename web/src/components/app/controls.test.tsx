// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
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
})
