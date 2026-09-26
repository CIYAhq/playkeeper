// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { actionsBeforePrompt, countDemoAction, Overlay } from './welcome'

let root: Root | undefined
let host: HTMLElement | undefined

async function show() {
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  await act(async () => root!.render(<Overlay />))
}

async function hide() {
  await act(async () => root?.unmount())
  host?.remove()
  root = undefined
}

function button(name: string): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>('button, a')].find((b) => (b.getAttribute('aria-label') ?? b.textContent ?? '').trim() === name)
}

beforeEach(() => localStorage.clear())
afterEach(hide)

it('says on the first visit that nothing is real, once', async () => {
  await show()
  expect(document.body.textContent).toContain('Playkeeper live demo')
  expect(document.body.textContent).toContain('nothing is real, and it all resets every hour')
  expect(button('Install on your VPS')?.getAttribute('href')).toBe('/#install')
  await act(async () => button('Start clicking')!.click())
  await hide()
  await show()
  expect(document.body.textContent).not.toContain('Playkeeper live demo')
})

it('asks quietly after a few of the demo’s actions, and not again once closed', async () => {
  localStorage.setItem('playkeeper-demo-welcomed', '1')
  await show()
  for (let i = 1; i < actionsBeforePrompt; i++) await act(async () => countDemoAction())
  expect(document.body.textContent).not.toContain('Like it so far?')
  await act(async () => countDemoAction())
  expect(document.body.textContent).toContain('Like it so far?')
  expect(button('Star on GitHub')?.getAttribute('href')).toBe('https://github.com/CIYAhq/playkeeper')
  expect(button('Questions? Ask in GitHub Discussions')?.getAttribute('href')).toBe('/community')
  await act(async () => button('Close')!.click())
  expect(document.body.textContent).not.toContain('Like it so far?')
  await act(async () => countDemoAction())
  expect(document.body.textContent).not.toContain('Like it so far?')
})
