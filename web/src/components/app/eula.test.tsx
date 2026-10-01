// @vitest-environment happy-dom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { EulaCheck } from '@/components/app/create'
import { Button } from '@/components/ui/button'

// The walkthrough of 1 Oct 2026: clicking the EULA sentence opened
// minecraft.net in a new tab and left the box unticked, and on a phone the
// disabled Create button said nothing when tapped.

let root: Root | undefined
let host: HTMLDivElement | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  host?.remove()
  root = host = undefined
})

async function render(node: React.ReactNode) {
  host = document.createElement('div')
  document.body.append(host)
  const r = createRoot(host)
  root = r
  await act(async () => r.render(node))
  return host
}

function Box({ short, nudge }: { short?: boolean; nudge?: string }) {
  const [checked, setChecked] = useState(false)
  return <EulaCheck checked={checked} onChange={setChecked} short={short} nudge={nudge} />
}

const checkbox = () => document.querySelector<HTMLElement>('[role="checkbox"]')

describe('The EULA box', () => {
  it('is ticked by its sentence, and named by it alone', async () => {
    await render(<Box />)
    const box = checkbox()
    const sentence = document.getElementById(box?.getAttribute('aria-labelledby') ?? '')
    expect(sentence?.textContent).toBe('I accept the Minecraft End User License Agreement')
    expect(box?.getAttribute('aria-checked')).toBe('false')
    await act(async () => sentence?.click())
    expect(box?.getAttribute('aria-checked')).toBe('true')
  })

  it('opens the EULA from a link of its own, which leaves the box as it is', async () => {
    await render(<Box short />)
    const link = document.querySelector<HTMLAnchorElement>('a')
    expect(link?.textContent).toBe('Read it')
    expect(link?.getAttribute('href')).toBe('https://www.minecraft.net/en-us/eula')
    expect(link?.getAttribute('target')).toBe('_blank')
    link?.addEventListener('click', (e) => e.preventDefault())
    await act(async () => link?.click())
    expect(checkbox()?.getAttribute('aria-checked')).toBe('false')
  })

  it('says what to do in place of its hint once a button it blocks was pressed', async () => {
    await render(<Box nudge="Tick this box first." />)
    const hint = document.getElementById(checkbox()?.getAttribute('aria-describedby') ?? '')
    expect(hint?.textContent).toBe('Tick this box first.')
    expect(hint?.getAttribute('role')).toBe('alert')
    expect(checkbox()?.getAttribute('aria-invalid')).toBe('true')
  })
})

describe('A button that says why it can’t be pressed yet', () => {
  it('stays focusable and pressable, and a press says why instead of acting', async () => {
    const act1 = vi.fn()
    const why = vi.fn()
    await render(
      <Button onClick={act1} disabledReason="Accept the Minecraft EULA first." onDisabledPress={why}>
        Create
      </Button>,
    )
    const button = document.querySelector('button')
    expect(button?.disabled).toBe(false)
    expect(button?.getAttribute('aria-disabled')).toBe('true')
    expect(button?.getAttribute('title')).toBe('Accept the Minecraft EULA first.')
    await act(async () => button?.click())
    expect(why).toHaveBeenCalledOnce()
    expect(act1).not.toHaveBeenCalled()
  })

  it('acts as usual once nothing blocks it', async () => {
    const act1 = vi.fn()
    const why = vi.fn()
    await render(
      <Button onClick={act1} onDisabledPress={why}>
        Create
      </Button>,
    )
    const button = document.querySelector('button')
    expect(button?.hasAttribute('aria-disabled')).toBe(false)
    await act(async () => button?.click())
    expect(act1).toHaveBeenCalledOnce()
    expect(why).not.toHaveBeenCalled()
  })

  it('is disabled outright without a way to say why', async () => {
    await render(<Button disabledReason="Busy.">Create</Button>)
    expect(document.querySelector('button')?.disabled).toBe(true)
  })
})
