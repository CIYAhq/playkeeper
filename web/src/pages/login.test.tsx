// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Challenge, Me, SecondFactorNeeded } from '@/api/types'
import { LoginPage } from './login'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  post: vi.fn(),
}))

const me: Me = {
  user: { username: 'siya', role: 'owner' },
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover'] },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
}

function asked(over: Partial<Challenge> = {}): SecondFactorNeeded {
  return { secondFactor: { methods: ['app_code', 'recovery_code'], appCodesBlocked: false, ...over }, user: { username: 'siya' }, expiresAt: '2026-09-25T12:05:00Z' }
}

const refusal = (status: number, code: string, retryAfter?: number) => new client.ApiError(status, { error: 'Refused.', code }, retryAfter)

function need<T>(x: T | null | undefined, what: string): T {
  if (x == null) throw new Error(`no ${what}`)
  return x
}

const text = () => document.body.textContent ?? ''
const boxes = () => [...document.querySelectorAll<HTMLInputElement>('input[inputmode="numeric"]')]
const box = (i: number) => need(boxes()[i], `box ${i}`)
const button = (label: string) => need([...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label), `${label} button`)

/** Types into an input the way a browser does, so React sees the change. */
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function submit() {
  await act(async () => {
    need(document.querySelector('form'), 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

let root: Root | undefined

/** Signs in with a password; the answer asks for the second step. */
async function secondStep(answer: SecondFactorNeeded = asked()) {
  const onDone = vi.fn()
  vi.mocked(client.post).mockResolvedValueOnce(answer)
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<LoginPage onDone={onDone} />))
  await type(need(document.querySelector<HTMLInputElement>('#login-username'), 'username'), 'siya')
  await type(need(document.querySelector<HTMLInputElement>('#login-password'), 'password'), 'correct horse')
  await submit()
  return onDone
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.post).mockReset()
})

describe('sign-in page', () => {
  const footer = () => need(document.querySelector('footer'), 'footer').textContent ?? ''

  it('names the machine and the Playkeeper version, through both steps', async () => {
    vi.mocked(client.post).mockResolvedValueOnce(asked())
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<LoginPage machine="my-vps" version="0.4.0" onDone={vi.fn()} />))
    expect(document.querySelector('h1')?.textContent).toBe('Sign in to Playkeeper')
    expect(text()).toContain('The dashboard for my-vps.')
    expect(footer()).toContain('Playkeeper 0.4.0')
    await type(need(document.querySelector<HTMLInputElement>('#login-username'), 'username'), 'siya')
    await type(need(document.querySelector<HTMLInputElement>('#login-password'), 'password'), 'correct horse')
    await submit()
    expect(text()).toContain('Enter your code')
    expect(footer()).toContain('Playkeeper 0.4.0')
  })

  it('leaves both out while they are not known', async () => {
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<LoginPage onDone={vi.fn()} />))
    expect(text()).toContain('Sign in to Playkeeper')
    expect(text()).not.toContain('The dashboard for')
    expect(footer()).not.toContain('Playkeeper')
  })
})

describe('second sign-in step', () => {
  it('asks for a code after the password and signs in with the sixth digit', async () => {
    const onDone = await secondStep()
    expect(client.post).toHaveBeenCalledWith('/api/auth/login', { username: 'siya', password: 'correct horse' })
    expect(text()).toContain('Enter your code')
    expect(text()).toContain('Signing in as siya')
    expect(boxes()).toHaveLength(6)
    vi.mocked(client.post).mockResolvedValueOnce(me)
    for (const [i, digit] of [...'482913'].entries()) {
      await type(box(i), digit)
      if (i < 5) expect(document.activeElement).toBe(box(i + 1))
    }
    expect(boxes().map((b) => b.value)).toEqual([...'482913'])
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/second-factor', { code: '482913' })
    expect(onDone).toHaveBeenCalledWith(me)
  })

  it('fills every box from a pasted code', async () => {
    await secondStep()
    vi.mocked(client.post).mockResolvedValueOnce(me)
    await act(async () => {
      const paste = new Event('paste', { bubbles: true, cancelable: true })
      Object.defineProperty(paste, 'clipboardData', { value: { getData: () => '482 913' } })
      box(0).dispatchEvent(paste)
    })
    expect(boxes().map((b) => b.value)).toEqual([...'482913'])
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/second-factor', { code: '482913' })
  })

  it('marks a wrong code, and typing starts a new one in the first box', async () => {
    await secondStep()
    vi.mocked(client.post).mockRejectedValueOnce(refusal(401, 'code_wrong'))
    await type(box(0), '482913')
    expect(text()).toContain('That code didn’t work. Try the one showing now.')
    expect(box(0).getAttribute('aria-invalid')).toBe('true')
    await type(box(3), '7')
    expect(boxes().map((b) => b.value)).toEqual(['7', '', '', '', '', ''])
    expect(text()).not.toContain('That code didn’t work')
  })

  it('starts the new code after a wrong one even when it begins with the digit already in the box', async () => {
    await secondStep()
    vi.mocked(client.post).mockRejectedValueOnce(refusal(401, 'code_wrong'))
    await type(box(0), '482913')
    box(0).focus()
    await act(async () => {
      box(0).dispatchEvent(new KeyboardEvent('keydown', { key: '4', bubbles: true, cancelable: true }))
    })
    expect(boxes().map((b) => b.value)).toEqual(['4', '', '', '', '', ''])
    expect(document.activeElement).toBe(box(1))
    expect(text()).not.toContain('That code didn’t work')
  })

  it('pauses app codes with a countdown while a recovery code still works', async () => {
    const onDone = await secondStep()
    expect(button('Sign in').title).toBe('Type all six digits first.')
    vi.mocked(client.post).mockRejectedValueOnce(refusal(429, 'app_codes_locked', 120))
    await type(box(0), '482913')
    expect(text()).toContain('Too many tries')
    expect(text()).toContain('Try again in 2:00, or use a recovery code.')
    expect(boxes().every((b) => b.disabled)).toBe(true)
    expect(button('Sign in').disabled).toBe(true)
    expect(button('Sign in').title).toBe('Try again in 2:00, or use a recovery code.')
    await click(button('Use a recovery code instead'))
    expect(text()).toContain('Use a recovery code')
    await type(need(document.querySelector<HTMLInputElement>('#recovery-code'), 'recovery code'), ' abcd-efgh-jkmn-pqrs ')
    vi.mocked(client.post).mockResolvedValueOnce(me)
    await submit()
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/second-factor', { code: 'abcd-efgh-jkmn-pqrs' })
    expect(onDone).toHaveBeenCalled()
  })

  it('starts paused when the password step says app codes are paused', async () => {
    await secondStep(asked({ appCodesLockedUntil: new Date(Date.now() + 61_000).toISOString() }))
    expect(text()).toMatch(/Try again in 1:0[01], or use a recovery code\./)
    expect(boxes().every((b) => b.disabled)).toBe(true)
  })

  it('goes straight to a recovery code once app codes are blocked', async () => {
    await secondStep(asked({ appCodesBlocked: true }))
    expect(text()).toContain('Use a recovery code')
    expect(text()).toContain('App codes are blocked after 100 wrong tries.')
    expect(text()).toContain('Signing in as siya')
    expect(boxes()).toHaveLength(0)
    expect(text()).not.toContain('Use your authenticator app instead')
  })

  it('names the reset command when app codes are blocked and no recovery codes are left', async () => {
    await secondStep(asked({ appCodesBlocked: true, methods: ['app_code'] }))
    expect(text()).toContain('No recovery codes are left. On the VPS run sudo playkeeper reset-2fa siya.')
    expect(button('Sign in').disabled).toBe(true)
    expect(button('Sign in').title).toBe('Run the command above on the VPS first.')
  })

  it('says a wrong recovery code plainly', async () => {
    await secondStep()
    await click(button('Use a recovery code instead'))
    await type(need(document.querySelector<HTMLInputElement>('#recovery-code'), 'recovery code'), 'abcd-efgh-jkmn-pqrs')
    vi.mocked(client.post).mockRejectedValueOnce(refusal(401, 'recovery_code_wrong'))
    await submit()
    expect(text()).toContain('That recovery code didn’t work.')
  })

  it('goes back to the password with the reason when the sign-in ran out', async () => {
    await secondStep()
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(401, { error: 'Please sign in again.', code: 'unauthorized' }))
    await type(box(0), '482913')
    expect(text()).toContain('Please sign in again.')
    expect(need(document.querySelector<HTMLInputElement>('#login-username'), 'username').value).toBe('siya')
  })

  it('cancels the pending sign-in for someone else', async () => {
    await secondStep()
    vi.mocked(client.post).mockResolvedValueOnce(undefined)
    await click(button('Not you?'))
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/second-factor/cancel')
    expect(need(document.querySelector<HTMLInputElement>('#login-username'), 'username').value).toBe('')
  })

  it('leaves the wrong codes typed here out of the notice about wrong codes', async () => {
    const onDone = await secondStep()
    vi.mocked(client.post).mockRejectedValueOnce(refusal(401, 'code_wrong'))
    await type(box(0), '111111')
    vi.mocked(client.post).mockResolvedValueOnce({ ...me, notices: [{ kind: 'failed_attempts', count: 1, text: '' }, { kind: 'recovery_codes_low', count: 2, text: '' }] })
    await type(box(0), '482913')
    expect(onDone).toHaveBeenLastCalledWith({ ...me, notices: [{ kind: 'recovery_codes_low', count: 2, text: '' }] })
  })

  it('keeps the wrong codes someone else typed', async () => {
    const onDone = await secondStep()
    vi.mocked(client.post).mockRejectedValueOnce(refusal(401, 'code_wrong'))
    await type(box(0), '111111')
    vi.mocked(client.post).mockResolvedValueOnce({ ...me, notices: [{ kind: 'failed_attempts', count: 4, text: '' }] })
    await type(box(0), '482913')
    expect(onDone).toHaveBeenLastCalledWith({ ...me, notices: [{ kind: 'failed_attempts', count: 3, text: '' }] })
  })
})
