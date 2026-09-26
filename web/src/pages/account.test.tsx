// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Me, TwoFactorSetup, TwoFactorStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { formatDate } from '@/lib/format'
import { useRoute } from '@/lib/router'
import { AccountPage } from './account'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
  post: vi.fn(),
  del: vi.fn(() => Promise.resolve(undefined)),
}))

const me: Me = {
  user: { username: 'siya', role: 'owner' },
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view'] },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  passwordChangedAt: new Date(Date.now() - 21 * 86_400_000).toISOString(),
}

const confirmedAt = '2026-09-12T10:00:00Z'
const off: TwoFactorStatus = { state: 'off', recoveryCodesLeft: 0, appCodesBlocked: false }
const pending: TwoFactorStatus = { state: 'pending', setupExpiresAt: '2026-09-25T12:10:00Z', recoveryCodesLeft: 0, appCodesBlocked: false }
const on: TwoFactorStatus = { state: 'on', confirmedAt, recoveryCodesLeft: 8, appCodesBlocked: false }
const fresh: TwoFactorStatus = { ...on, recoveryCodesLeft: 10 }

const setup: TwoFactorSetup = {
  qrCodeSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 29 29"><rect width="29" height="29" fill="#fff"/></svg>',
  manualKey: '4KQZ 7MXP 2RDN 6WYA 5HTB 3JCE LN2V QF7S',
  uri: 'otpauth://totp/Playkeeper:siya%40203.0.113.10?secret=4KQZ7MXP2RDN6WYA5HTB3JCELN2VQF7S&issuer=Playkeeper',
  issuer: 'Playkeeper',
  account: 'siya@203.0.113.10',
  expiresAt: '2026-09-25T12:10:00Z',
}

const codes = ['j3kz-v9nq-sgck-p635', 'bdw2-fzav-ut2m-5tx8', 'fk4s-des2-2h82-5grf', 'mpwx-mqus-rfad-59vx', 'ynzm-wp6s-ben9-2cuw', '73h4-2dp2-my6k-tajr', 'c3ud-m3hx-eeuu-btdw', '9t26-2ypj-sgpy-6gus', 'fuey-zjy5-s6g8-effd', '2hfz-6cny-76dn-v3yb']

const refusal = (status: number, code: string, error = 'Refused.', hint?: string) => new client.ApiError(status, { error, code, hint })

function need<T>(x: T | null | undefined, what: string): T {
  if (x == null) throw new Error(`no ${what}`)
  return x
}

const text = () => document.body.textContent ?? ''
const boxes = () => [...document.querySelectorAll<HTMLInputElement>('input[inputmode="numeric"]')]
const box = (i: number) => need(boxes()[i], `box ${i}`)
const button = (label: string) => need([...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label), `${label} button`)
const link = (label: string) => need([...document.querySelectorAll('a')].find((a) => a.textContent?.trim() === label), `${label} link`)
const password = () => need(document.querySelector<HTMLInputElement>('input[type="password"]'), 'password field')
const qr = () => document.querySelector('img[alt="QR code for your authenticator app"]')

function field(label: string): HTMLInputElement {
  const l = need([...document.querySelectorAll('label')].find((x) => x.textContent?.trim() === label), `${label} label`)
  return need(document.getElementById(l.htmlFor) as HTMLInputElement | null, `${label} field`)
}

/** Types into an input the way a browser does, so React sees the change. */
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function typeCode(code: string) {
  for (const [i, digit] of [...code].entries()) await type(box(i), digit)
}

async function submit() {
  await act(async () => {
    need(document.querySelector('form'), 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

async function escape() {
  await act(async () => {
    ;(document.activeElement ?? document.body).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  })
}

/** The status the page reads; a started setup answers too. */
function answer(status: TwoFactorStatus) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path === '/api/auth/2fa') return Promise.resolve(status)
    if (path === '/api/auth/2fa/setup' && status.state === 'pending') return Promise.resolve(setup)
    return Promise.reject(refusal(409, 'setup_missing'))
  }) as typeof client.get)
}

function asPhone() {
  const real = window.matchMedia.bind(window)
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
    const list = real(query)
    if (query === '(max-width: 639px)') Object.defineProperty(list, 'matches', { value: true })
    return list
  })
}

function Routed() {
  const route = useRoute()
  return route.name === 'account' ? <AccountPage section={route.section} /> : null
}

let root: Root | undefined

async function show(status: TwoFactorStatus, path = '/account', over: Partial<Workspace> = {}) {
  answer(status)
  window.history.replaceState(null, '', path)
  const ws = { me, signOut: vi.fn(async () => {}), reloadMe: vi.fn(async () => {}), ...over } as unknown as Workspace
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{<Routed />}</WorkspaceContext.Provider>))
  await act(async () => {})
  return ws
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
  vi.mocked(client.del).mockClear()
  vi.restoreAllMocks()
})

describe('Account page', () => {
  it('shows two-factor off, and Turn on opens the setup', async () => {
    await show(off)
    expect(text()).toContain('Your account')
    expect(text()).toContain('Signed in as siya · Admin')
    expect(text()).toContain('Last changed 3 weeks ago.')
    expect(text()).toContain('A code from your phone as well as your password.')
    expect(text()).not.toContain('Recovery codes')
    expect(link('Turn on').getAttribute('href')).toBe('/account/two-factor')
    await click(link('Turn on'))
    expect(window.location.pathname).toBe('/account/two-factor')
    expect(text()).toContain('Turn on two-factor sign-in')
    expect(text()).toContain('Step 1 of 3')
  })

  it('shows since when two-factor is on and how many recovery codes are left', async () => {
    await show(on)
    expect(text()).toContain(`On since ${formatDate(confirmedAt)}.`)
    expect(text()).toContain('8 of 10 left.')
    expect(button('Turn off')).toBeTruthy()
    expect(button('Make new codes')).toBeTruthy()
  })

  it('leaves the setup page when two-factor is already on', async () => {
    await show(on, '/account/two-factor')
    expect(window.location.pathname).toBe('/account')
    expect(text()).not.toContain('Step 1 of 3')
  })
})

describe('turning two-factor on', () => {
  it('asks for the password, then a code, then shows recovery codes until they are saved', async () => {
    const toast = vi.spyOn(toastManager, 'add')
    await show(off, '/account/two-factor')
    vi.mocked(client.post).mockResolvedValueOnce(setup)
    await type(password(), 'correct horse')
    await submit()
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/2fa/setup', { password: 'correct horse' })
    expect(text()).toContain('Step 2 of 3')
    expect(text()).toContain(setup.manualKey)
    expect(qr()?.getAttribute('src')).toMatch(/^data:image\/svg\+xml;charset=utf-8,%3Csvg/)

    vi.mocked(client.post).mockResolvedValueOnce({ recoveryCodes: codes, status: fresh })
    await typeCode('482913')
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/2fa/confirm', { code: '482913' })
    expect(text()).toContain('Save your recovery codes')
    expect(text()).toContain('Step 3 of 3')
    for (const c of codes) expect(text()).toContain(c)

    await escape()
    expect(window.location.pathname).toBe('/account/two-factor')
    expect(text()).toContain('Save your recovery codes')

    answer(fresh)
    await click(button('I’ve saved them'))
    expect(window.location.pathname).toBe('/account')
    expect(toast).toHaveBeenCalledWith(expect.objectContaining({ title: 'Two-factor sign-in is on. Your other sessions were signed out.' }))
    expect(text()).toContain('10 of 10 left.')
  })

  it('says so under the field when the password is wrong', async () => {
    await show(off, '/account/two-factor')
    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'password_wrong', 'Your password is not correct.'))
    await type(password(), 'nope')
    await submit()
    expect(text()).toContain('Your password is not correct.')
    expect(password().getAttribute('aria-invalid')).toBe('true')
    expect(text()).toContain('Step 1 of 3')
  })

  it('marks a wrong code, and starts again at the password when the setup expired', async () => {
    await show(off, '/account/two-factor')
    vi.mocked(client.post).mockResolvedValueOnce(setup)
    await type(password(), 'correct horse')
    await submit()

    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'code_wrong', 'That code is not correct.'))
    await typeCode('111111')
    expect(text()).toContain('That code didn’t work. Try the one showing now.')
    expect(box(0).getAttribute('aria-invalid')).toBe('true')

    vi.mocked(client.post).mockRejectedValueOnce(refusal(409, 'setup_expired', 'This two-factor setup has expired.', 'Start again to get a new QR code.'))
    await typeCode('222222')
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/2fa/confirm', { code: '222222' })
    expect(text()).toContain('Step 1 of 3')
    expect(text()).toContain('This two-factor setup has expired. Start again to get a new QR code.')
  })

  it('keeps an unfinished setup when closed, picks it up at the QR code, and throws it away on Cancel', async () => {
    await show(off, '/account/two-factor')
    vi.mocked(client.post).mockResolvedValueOnce(setup)
    await type(password(), 'correct horse')
    await submit()
    answer(pending)
    await escape()
    expect(window.location.pathname).toBe('/account')
    expect(client.del).not.toHaveBeenCalled()

    await click(link('Turn on'))
    expect(client.get).toHaveBeenCalledWith('/api/auth/2fa/setup')
    expect(text()).toContain('Step 2 of 3')
    await click(button('Cancel'))
    expect(client.del).toHaveBeenCalledWith('/api/auth/2fa/setup')
    expect(window.location.pathname).toBe('/account')
  })

  it('has a page of its own on a phone, with the QR code behind a row', async () => {
    asPhone()
    await show(off, '/account/two-factor')
    expect(link('Account').getAttribute('href')).toBe('/account')
    expect(text()).toContain('Step 1 of 3')
    vi.mocked(client.post).mockResolvedValueOnce(setup)
    await type(password(), 'correct horse')
    await submit()
    expect(text()).toContain('Add Playkeeper to your authenticator app')
    expect(link('Open authenticator app').getAttribute('href')).toBe(setup.uri)
    expect(qr()).toBeNull()
    await click(button('Show a QR code'))
    expect(qr()).not.toBeNull()

    vi.mocked(client.post).mockResolvedValueOnce({ recoveryCodes: codes, status: fresh })
    await typeCode('482913')
    expect(text()).toContain('Save these codes')
    expect(button('Download')).toBeTruthy()
    answer(fresh)
    await click(button('I’ve saved them'))
    expect(window.location.pathname).toBe('/account')
    expect(text()).toContain('10 of 10 left')
  })
})

describe('with two-factor on', () => {
  it('makes new recovery codes after the password and a code, and shows them once', async () => {
    await show(on)
    await click(button('Make new codes'))
    expect(text()).toContain('Make new recovery codes?')
    expect(text()).toContain('Your 8 unused codes stop working.')

    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'password_wrong', 'Your password is not correct.'))
    await type(password(), 'nope')
    await typeCode('419372')
    expect(client.post).not.toHaveBeenCalled()
    await submit()
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/2fa/recovery-codes', { password: 'nope', code: '419372' })
    expect(password().getAttribute('aria-invalid')).toBe('true')

    answer(fresh)
    vi.mocked(client.post).mockResolvedValueOnce({ recoveryCodes: codes })
    await type(password(), 'correct horse')
    await submit()
    expect(text()).toContain('Save your recovery codes')
    expect(text()).not.toContain('Step 3 of 3')
    for (const c of codes) expect(text()).toContain(c)
    expect(text()).toContain('10 of 10 left.')

    await escape()
    expect(text()).toContain('Save your recovery codes')
    await click(button('I’ve saved them'))
    expect(text()).not.toContain('Save your recovery codes')
  })

  it('turns two-factor off, taking a recovery code while app codes are paused', async () => {
    const toast = vi.spyOn(toastManager, 'add')
    await show(on)
    await click(button('Turn off'))
    expect(text()).toContain('Turn off two-factor sign-in?')
    expect(text()).toContain('Signing in will need only your password.')

    vi.mocked(client.post).mockRejectedValueOnce(refusal(429, 'app_codes_locked', 'Too many wrong codes. Try again in 16 minutes.', 'You can use a recovery code now instead.'))
    await type(password(), 'correct horse')
    await typeCode('731904')
    await submit()
    expect(text()).toContain('Too many wrong codes. Try again in 16 minutes. You can use a recovery code now instead.')

    await click(button('Use a recovery code instead'))
    const recovery = need(document.querySelector<HTMLInputElement>('input[placeholder="abcd-efgh-jkmn-pqrs"]'), 'recovery field')
    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'recovery_code_wrong'))
    await type(recovery, ' abcd-efgh-jkmn-pqrs ')
    await submit()
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/2fa/disable', { password: 'correct horse', code: 'abcd-efgh-jkmn-pqrs' })
    expect(text()).toContain('That recovery code didn’t work.')

    answer(off)
    vi.mocked(client.post).mockResolvedValueOnce(undefined)
    await type(recovery, 'j3kz-v9nq-sgck-p635')
    await submit()
    expect(toast).toHaveBeenCalledWith(expect.objectContaining({ title: 'Two-factor sign-in is off.' }))
    expect(link('Turn on')).toBeTruthy()
  })

  it('lists the account as rows on a phone, with Turn off in red', async () => {
    asPhone()
    await show(on)
    expect(link('More').getAttribute('href')).toBe('/more')
    expect(text()).toContain('Changed 3 weeks ago')
    expect(text()).toContain(`On since ${formatDate(confirmedAt)}`)
    expect(text()).toContain('8 of 10 left')
    expect(button('Sign out everywhere')).toBeTruthy()
    await click(button('Turn off two-factor'))
    expect(text()).toContain('Turn off two-factor sign-in?')
  })
})

describe('changing the password', () => {
  it('opens from the link in the notice after signing in, and reloads who is signed in', async () => {
    const reloadMe = vi.fn(async () => {})
    await show(off, '/account#password', { reloadMe })
    expect(window.location.hash).toBe('')
    expect(text()).toContain('Other browsers and phones are signed out.')

    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'forbidden', 'Your current password is not correct.'))
    await type(field('Current password'), 'wrong')
    await type(field('New password'), 'a much longer one')
    await submit()
    expect(text()).toContain('Your current password is not correct.')
    expect(field('Current password').getAttribute('aria-invalid')).toBe('true')

    vi.mocked(client.post).mockResolvedValueOnce(undefined)
    await type(field('Current password'), 'correct horse')
    await submit()
    expect(client.post).toHaveBeenLastCalledWith('/api/auth/password', { currentPassword: 'correct horse', newPassword: 'a much longer one' })
    expect(reloadMe).toHaveBeenCalled()
  })
})
