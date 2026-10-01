// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Address, AddressCheck, AddressPlan, DNSAnswers, DNSRecord, FreeAddress, JoinAddress, MachineView, Me, NameAvailability, Operation, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { formatDate, formatDateTime, formatLongDate } from '@/lib/format'
import { MachineSettingsPage } from '.'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

const me: Me = {
  user: { username: 'siya', role: 'owner' },
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover'] },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
}
const machine = { id: 'm1', projectId: 'p1', name: 'my-vps', kind: 'local' } as MachineView
const servers = [
  { id: 's1', name: 'Survival', slug: 'survival' },
  { id: 's2', name: 'Creative', slug: 'creative' },
] as ServerStatus[]

const ip = '198.51.100.10'
const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const claimedAt = ago(30_000)
const notAfter = new Date(Date.now() + 90 * 86_400_000).toISOString()

const survival = (over: Partial<JoinAddress> = {}): JoinAddress => ({ serverId: 's1', name: 'Survival', port: 25565, label: '', direct: ip, published: false, ...over })
const creative = (over: Partial<JoinAddress> = {}): JoinAddress => ({ serverId: 's2', name: 'Creative', port: 25566, label: '', direct: `${ip}:25566`, published: false, ...over })
const op = (over: Partial<Operation>): Operation => ({ id: 'op1', kind: 'address.publish', status: 'running', phase: 'pointing', actor: 'siya', startedAt: ago(29_000), ...over })

const none: Address = { kind: '', ip, panelPort: 8443, base: 'playkeeper.me', previousBase: 'playkeeper.io', servers: [survival(), creative()], names: { url: 'https://names.playkeeper.io' } }

function free(over: Partial<Address> = {}, name: Partial<FreeAddress> = {}): Address {
  return {
    ...none,
    kind: 'playkeeper',
    host: 'alex.playkeeper.me',
    termsAccepted: claimedAt,
    servers: [survival({ label: 'survival', address: 'survival.alex.playkeeper.me', published: true }), creative({ label: 'creative', address: 'creative.alex.playkeeper.me', published: true })],
    free: { name: 'alex', state: 'active', dns: 'ok', claimedAt, refreshedAt: claimedAt, checkedAt: claimedAt, holdDays: 30, ...name },
    certificate: { names: ['alex.playkeeper.me'], challenge: 'dns-01', notAfter },
    ...over,
  }
}

const aRecord: DNSRecord = { type: 'A', name: 'play.example.com', value: ip, ttl: 300 }
const srvRecord: DNSRecord = {
  serverId: 's2',
  type: 'SRV',
  name: '_minecraft._tcp.creative.play.example.com',
  value: '0 5 25566 play.example.com',
  ttl: 300,
  srv: { service: 'minecraft', protocol: 'tcp', host: 'creative.play.example.com', priority: 0, weight: 5, port: 25566, target: 'play.example.com' },
}
const plan: AddressPlan = { domain: 'play.example.com', records: [aRecord, srvRecord], servers: [survival({ address: 'play.example.com' }), creative({ label: 'creative', address: 'creative.play.example.com' })] }

function check(over: Partial<AddressCheck['name']> = {}, ready = true, records: AddressCheck['records'] = [{ record: srvRecord, ok: true, code: 'srv_ok', message: 'The SRV record is right.' }]): AddressCheck {
  return { at: new Date().toISOString(), name: { name: 'play.example.com', ok: true, code: 'name_ok', message: 'It points here.', records: [{ type: 'A', addr: ip, here: true }], ...over }, records, ready }
}

function own(over: Partial<Address> = {}): Address {
  return {
    ...none,
    kind: 'own',
    host: 'play.example.com',
    termsAccepted: claimedAt,
    servers: [survival({ address: 'play.example.com', published: true }), creative({ label: 'creative', address: 'creative.play.example.com', published: true })],
    records: [aRecord, srvRecord],
    check: check(),
    certificate: { names: ['play.example.com'], challenge: 'http-01', notAfter },
    ...over,
  }
}

const refusal = (status: number, code: string, error = 'Refused.', params?: Record<string, unknown>, hint?: string) => new client.ApiError(status, { error, code, hint, params })

function need<T>(x: T | null | undefined, what: string): T {
  if (x == null) throw new Error(`no ${what}`)
  return x
}

const text = () => document.body.textContent ?? ''

/** What axe's list rule refuses: anything but li, script and template directly in a list. */
function strayListChildren(): string[] {
  return [...document.querySelectorAll('ul, ol')].flatMap((l) => [...l.children].filter((c) => !['LI', 'SCRIPT', 'TEMPLATE'].includes(c.tagName)).map((c) => c.tagName))
}
const buttons = (label: string, within: ParentNode = document) => [...within.querySelectorAll('button')].filter((b) => b.textContent?.trim() === label)
const button = (label: string, within: ParentNode = document) => need(buttons(label, within)[0], `${label} button`)
const link = (label: string) => need([...document.querySelectorAll('a')].find((a) => a.textContent?.trim() === label), `${label} link`)
const dialog = () => need(document.querySelector('[role="dialog"]'), 'dialog')
/** The switch in the row that says label. */
const switchIn = (label: string) => need([...document.querySelectorAll<HTMLElement>('[role="switch"]')].find((x) => x.parentElement?.textContent?.includes(label)), `${label} switch`)
const currentStep = () => document.querySelector('[aria-current="step"]')?.textContent ?? ''

function field(label: string): HTMLInputElement {
  const l = need([...document.querySelectorAll('label')].find((x) => x.textContent?.trim() === label), `${label} label`)
  return need(document.getElementById(l.htmlFor) as HTMLInputElement | null, `${label} field`)
}

function radio(label: string): HTMLElement {
  const l = need([...document.querySelectorAll('label')].find((x) => x.textContent?.includes(label)), `${label} choice`)
  return need(l.querySelector<HTMLElement>('[role="radio"]'), `${label} radio`)
}

async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

/** Lets a lookup's pause after typing pass, and its answer arrive. */
async function settle(ms = 600) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms))
  })
}

function asPhone() {
  const real = window.matchMedia.bind(window)
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
    const list = real(query)
    if (query === '(max-width: 639px)') Object.defineProperty(list, 'matches', { value: true })
    return list
  })
}

let current: Address | client.ApiError = none
let names: Record<string, NameAvailability | client.ApiError> = {}
let dns: DNSAnswers | undefined
let root: Root | undefined

function answerGets() {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path === '/api/machines/m1/address') return current instanceof client.ApiError ? Promise.reject(current) : Promise.resolve(current)
    const name = decodeURIComponent(path.match(/\/address\/available\?name=(.*)$/)?.[1] ?? '')
    if (name) {
      const r = names[name] ?? { name, address: `${name}.playkeeper.me`, available: true }
      return r instanceof client.ApiError ? Promise.reject(r) : Promise.resolve(r)
    }
    if (path === '/api/machines/m1/address/plan?domain=play.example.com') return Promise.resolve(plan)
    if (path === '/api/dns-answers' && dns) return Promise.resolve(dns)
    return Promise.reject(refusal(404, 'not_found'))
  }) as typeof client.get)
}

async function show(a: Address | client.ApiError, { phone = false, kind = 'local' } = {}) {
  if (phone) asPhone()
  current = a
  answerGets()
  const m = { ...machine, kind } as MachineView
  const ws = { me, machines: [m], machine: m, servers, machineName: 'my-vps' } as unknown as Workspace
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{<MachineSettingsPage id="m1" />}</WorkspaceContext.Provider>))
  await act(async () => {})
}

const availableCalls = () => vi.mocked(client.get).mock.calls.filter(([p]) => p.includes('/address/available')).length

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  names = {}
  dns = undefined
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
  vi.mocked(client.put).mockReset()
  vi.mocked(client.del).mockReset()
  vi.restoreAllMocks()
})

describe('choosing an address', () => {
  it('starts with a free name, checks it as it is typed and shows the addresses it gives', async () => {
    await show(none)
    expect(text()).toContain('Machine settings')
    expect(text()).toContain(`my-vps · ${ip}`)
    expect(text()).toContain(`A name instead of ${ip}, and no more browser warnings.`)
    expect(radio('Free playkeeper.me address').getAttribute('aria-checked')).toBe('true')
    expect(field('Pick a name').value).toBe('siya')
    expect(text()).toContain('Checking siya.playkeeper.me…')
    await settle()
    expect(text()).toContain('siya.playkeeper.me is free')
    expect(text()).toContain('survival.siya.playkeeper.me')
    expect(text()).toContain('creative.siya.playkeeper.me')
    expect(text()).toContain('https://siya.playkeeper.me:8443')
    expect(text()).toContain('By continuing you accept Let’s Encrypt’s terms.')
    expect(button('Claim siya.playkeeper.me').disabled).toBe(false)
  })

  it('names the domain free names live under as the agent says it', async () => {
    await show({ ...none, base: 'names.example.net' })
    expect(radio('Free names.example.net address').getAttribute('aria-checked')).toBe('true')
    expect(text()).toContain('Like alex.names.example.net. Ready in a minute.')
    await settle()
    expect(text()).toContain('siya.names.example.net is free')
  })

  it('says why a name can’t be had: not allowed, taken with free ones, held and reserved', async () => {
    names = {
      alex: { name: 'alex', address: 'alex.playkeeper.me', available: false, code: 'name_taken', message: 'Taken.', suggestions: ['alex-mc', 'alexcraft', 'alex-plays'] },
      'steve-mc': { name: 'steve-mc', address: 'steve-mc.playkeeper.me', available: false, code: 'name_held', message: 'Held.', params: { until: Date.parse('2026-10-24T12:00:00Z') / 1000 } },
      admin: { name: 'admin', address: 'admin.playkeeper.me', available: false, code: 'name_reserved', message: 'Reserved.' },
    }
    await show(none)
    await type(field('Pick a name'), 'alex--mc')
    expect(text()).toContain('Use a–z, 0–9 and single dashes, like alex-mc.')
    expect(field('Pick a name').getAttribute('aria-invalid')).toBe('true')
    expect(button('Claim').disabled).toBe(true)
    expect(button('Claim').title).toBe('Use a–z, 0–9 and single dashes, like alex-mc.')

    await type(field('Pick a name'), 'alex')
    expect(text()).toContain('Checking alex.playkeeper.me…')
    await settle()
    expect(text()).toContain('Someone already has alex.playkeeper.me.')
    expect(text()).toContain('Free right now:')
    expect(button('Claim alex.playkeeper.me').disabled).toBe(true)
    expect(button('Claim alex.playkeeper.me').title).toBe('Pick a name that’s free.')
    await click(button('alex-mc'))
    expect(field('Pick a name').value).toBe('alex-mc')
    await settle()
    expect(text()).toContain('alex-mc.playkeeper.me is free')

    await type(field('Pick a name'), 'Steve-MC.playkeeper.me')
    await settle()
    expect(text()).toContain(`steve-mc.playkeeper.me is held until ${formatDate('2026-10-24T12:00:00Z')}.`)
    // An address pasted from before free names moved gives the same name.
    await type(field('Pick a name'), 'Steve-MC.playkeeper.io')
    await settle()
    expect(text()).toContain(`steve-mc.playkeeper.me is held until ${formatDate('2026-10-24T12:00:00Z')}.`)
    expect(link('Learn more').getAttribute('href')).toBe('https://github.com/CIYAhq/playkeeper#a-name-for-your-vps')

    await type(field('Pick a name'), 'admin')
    await settle()
    expect(text()).toContain('admin.playkeeper.me is reserved. Pick another name.')
  })

  it('claims a name in three steps', async () => {
    let finish: (a: Address) => void = () => {}
    vi.mocked(client.post).mockImplementation((() => new Promise<Address>((r) => (finish = r))) as typeof client.post)
    await show(none)
    await type(field('Pick a name'), 'alex')
    await settle()
    await click(button('Claim alex.playkeeper.me'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/claim', { name: 'alex', acceptTerms: true })
    expect(text()).toContain('Claiming alex.playkeeper.me')
    expect(currentStep()).toContain('Name reserved')

    current = free({ operation: op({ phase: 'pointing' }) })
    await act(async () => finish(current as Address))
    await act(async () => {})
    expect(text()).toContain('Claiming alex.playkeeper.me')
    expect(currentStep()).toContain('Pointing it at my-vps')
    expect(currentStep()).toContain(ip)
  })

  it('picks up a claim in progress at its certificate step', async () => {
    await show(free({ operation: op({ phase: 'certificate' }) }))
    expect(currentStep()).toContain('Getting a certificate')
    expect(currentStep()).toContain('Usually under a minute')
  })

  it('counts down when the service asks to wait, and moves on to a domain when it is full', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(refusal(429, 'rate_limited', 'Too many requests.', { retryAfterSeconds: 2652 }))
    await show(none)
    await settle()
    await click(button('Claim siya.playkeeper.me'))
    expect(text()).toContain('Too many tries from this machine')
    expect(button('Try again in 44:12').disabled).toBe(true)
    expect(button('Try again in 44:12').title).toBe('Try again in 44:12')

    vi.mocked(client.post).mockRejectedValueOnce(refusal(507, 'zone_full', 'Full.'))
    await type(field('Pick a name'), 'alex')
    await settle()
    await click(button('Claim alex.playkeeper.me'))
    expect(text()).toContain('The free address service is full right now')
    expect(text()).toContain('Try again tomorrow, or use your own domain.')
    expect(field('Pick a name').value).toBe('alex')
    expect(text()).toContain('survival.alex.playkeeper.me')
    await click(button('Use your own domain'))
    expect(radio('Your own domain').getAttribute('aria-checked')).toBe('true')
    expect(field('Your domain')).toBeTruthy()
  })

  it('gives the command for a clock that is off, and Try again claims again', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(refusal(502, 'clock_skew', 'Clock skew.', { skewSeconds: -540 }))
    await show(none)
    await settle()
    await click(button('Claim siya.playkeeper.me'))
    expect(text()).toContain('my-vps’s clock is 9 minutes off')
    expect(text()).toContain('Run this on the VPS, then try again:')
    expect(text()).toContain('sudo timedatectl set-ntp true')
    expect(document.querySelector('[aria-label="Copy sudo timedatectl set-ntp true"]')).toBeTruthy()
    vi.mocked(client.post).mockRejectedValueOnce(refusal(403, 'not_public_address', 'Not public.', { ip: '10.0.0.5' }))
    await click(button('Try again'))
    expect(client.post).toHaveBeenCalledTimes(2)
    expect(client.post).toHaveBeenLastCalledWith('/api/machines/m1/address/claim', { name: 'siya', acceptTerms: true })
    expect(text()).toContain('my-vps isn’t on a public address')
    expect(text()).toContain('The free address service saw 10.0.0.5, which a name can’t point at.')
  })

  it('says when the free address service couldn’t reach the dashboard on port 8443, with Try again and how to open it', async () => {
    vi.mocked(client.post).mockRejectedValue(refusal(409, 'not_answering', 'siya.playkeeper.me stays off.', { name: 'siya', ip, port: 8443 }))
    await show(none)
    await settle()
    await click(button('Claim siya.playkeeper.me'))
    expect(text()).toContain('The free address service couldn’t reach my-vps')
    expect(text()).toContain('Open port 8443 in your VPS provider’s firewall, then try again.')
    expect(text()).not.toContain('stays off')
    expect(link('How to open a port').getAttribute('href')).toBe('https://playkeeper.io/docs/troubleshooting#friends-cant-join')
    await click(button('Try again'))
    expect(client.post).toHaveBeenCalledTimes(2)
    expect(client.post).toHaveBeenLastCalledWith('/api/machines/m1/address/claim', { name: 'siya', acceptTerms: true })
  })

  it('says in one quiet line when free addresses aren’t available, and picks up once the service answers', async () => {
    names = { siya: refusal(503, 'names_unreachable', 'Playkeeper couldn’t reach the free address service.', { detail: 'dial tcp: lookup names.playkeeper.io: no such host' }) }
    await show(none)
    await settle()
    expect(text()).toContain('Free addresses aren’t available right now. Try again')
    expect(text()).not.toContain('couldn’t reach')
    expect(text()).not.toContain('no such host')
    expect(document.querySelector('[role="alert"]')).toBeNull()
    expect(button('Claim siya.playkeeper.me').disabled).toBe(true)
    expect(button('Claim siya.playkeeper.me').title).toBe('Free addresses aren’t available right now.')
    const preview = need([...document.querySelectorAll('dd')].find((d) => d.textContent === 'https://siya.playkeeper.me:8443'), 'dashboard preview')
    expect(preview.className).toContain('text-muted-foreground')

    const before = availableCalls()
    await click(button('Try again'))
    expect(text()).toContain('Checking siya.playkeeper.me…')
    await settle()
    expect(availableCalls()).toBe(before + 1)
    expect(text()).toContain('Free addresses aren’t available right now.')

    delete names.siya
    await click(button('Try again'))
    await settle()
    expect(text()).toContain('siya.playkeeper.me is free')
    expect(text()).not.toContain('aren’t available')
    expect(button('Claim siya.playkeeper.me').disabled).toBe(false)
    expect(preview.className).not.toContain('text-muted-foreground')
  })

  it('keeps the own domain one tap away while free addresses aren’t available', async () => {
    names = { siya: refusal(503, 'names_unreachable', 'Playkeeper couldn’t reach the free address service.') }
    await show(none)
    await settle()
    expect(buttons('Use your own domain')).toHaveLength(0)
    await click(radio('Your own domain'))
    expect(field('Your domain')).toBeTruthy()
  })

  it('says the same on a phone, with Claim waiting at the bottom', async () => {
    names = { siya: refusal(503, 'names_unreachable', 'Playkeeper couldn’t reach the free address service.') }
    await show(none, { phone: true })
    await settle()
    expect(text()).toContain('Free addresses aren’t available right now. Try again')
    expect(text()).not.toContain('3 to 32 lowercase letters')
    expect(document.querySelector('[role="alert"]')).toBeNull()
    expect(button('Claim siya.playkeeper.me').disabled).toBe(true)
  })

  it('says the same when the service stops answering between the check and the claim, and Try again claims again', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(refusal(503, 'names_unreachable', 'Playkeeper couldn’t reach the free address service.'))
    await show(none)
    await settle()
    await click(button('Claim siya.playkeeper.me'))
    expect(text()).toContain('Free addresses aren’t available right now. Try again')
    expect(document.querySelector('[role="alert"]')).toBeNull()
    vi.mocked(client.post).mockResolvedValueOnce(free())
    await click(button('Try again'))
    expect(client.post).toHaveBeenCalledTimes(2)
    expect(client.post).toHaveBeenLastCalledWith('/api/machines/m1/address/claim', { name: 'siya', acceptTerms: true })
  })

  it('offers Try again when the address can’t be loaded', async () => {
    await show(refusal(502, 'agent_unreachable', 'The agent isn’t answering.'))
    expect(text()).toContain('Couldn’t load the address')
    expect(text()).toContain('The agent isn’t answering.')
    current = none
    await click(button('Try again'))
    expect(text()).toContain('Pick a name')
  })
})

describe('a free address', () => {
  it('shows each join address, the dashboard with its port, and Open', async () => {
    await show(free())
    expect(text()).toContain('alex.playkeeper.me is yours')
    expect(text()).toContain('Real certificate, renews by itself.')
    const open = link('Open https://alex.playkeeper.me:8443')
    expect(open.getAttribute('href')).toBe('https://alex.playkeeper.me:8443')
    expect(open.getAttribute('target')).toBe('_blank')
    expect(text()).toContain('You’ll sign in again there.')
    for (const value of ['survival.alex.playkeeper.me', 'creative.alex.playkeeper.me', 'https://alex.playkeeper.me:8443']) {
      expect(document.querySelector(`[aria-label="Copy ${value}"]`), value).toBeTruthy()
    }
    expect(text()).toContain(`${ip} keeps working too.`)
  })

  it('shows publishing with each address’s state, and Open waits', async () => {
    await show(free({ servers: [survival({ label: 'survival', address: 'survival.alex.playkeeper.me', published: true }), creative({ label: 'creative', address: 'creative.alex.playkeeper.me' })], operation: op({ phase: 'publishing' }) }))
    expect(text()).toContain('Publishing alex.playkeeper.me…')
    expect(text()).toContain('Usually a minute or two.')
    expect(text()).toContain('published')
    expect(text()).toContain('publishing…')
    expect(button('Open https://alex.playkeeper.me:8443').disabled).toBe(true)
    expect(button('Open https://alex.playkeeper.me:8443').title).toBe('Opens once it’s published.')
  })

  it('warns when it lapsed, and Refresh brings it back', async () => {
    const stoppedAt = '2026-10-03T09:00:00Z'
    vi.mocked(client.post).mockResolvedValue(free({ operation: op({ startedAt: new Date().toISOString() }) }))
    await show(free({}, { state: 'lapsed', stoppedAt }))
    expect(text()).toContain(`This address stopped updating on ${formatDate(stoppedAt)}`)
    expect(text()).toContain('my-vps was offline for about a month. Refresh to bring it back.')
    expect(text()).toContain('not updating')
    await click(button('Refresh'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/refresh', { acceptTerms: true })
  })

  it('changes the name, and Cancel keeps the old one', async () => {
    await show(free())
    await click(button('Change name'))
    expect(text()).toContain('alex.playkeeper.me stops working when you claim another name.')
    expect(field('Pick a name').value).toBe('')
    await type(field('Pick a name'), 'alex')
    expect(text()).toContain('alex.playkeeper.me is yours')
    expect(button('Claim alex.playkeeper.me').disabled).toBe(true)
    await click(button('Cancel'))
    expect(link('Open https://alex.playkeeper.me:8443')).toBeTruthy()
  })

  it('asks before releasing, then says the name is released', async () => {
    const add = vi.spyOn(toastManager, 'add')
    vi.mocked(client.post).mockImplementation((() => {
      current = none
      return Promise.resolve(none)
    }) as typeof client.post)
    await show(free())
    await click(button('Release it'))
    expect(dialog().textContent).toContain('Release alex.playkeeper.me?')
    expect(dialog().textContent).toContain('The addresses stop working right away. The name is held for 30 days.')
    await click(button('Release it', dialog()))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/release', {})
    expect(add).toHaveBeenCalledWith(expect.objectContaining({ title: 'alex.playkeeper.me is released', type: 'success' }))
    expect(text()).toContain('Pick a name')
  })

  it('shows a certificate problem with Try again, as the one notice even while the service doesn’t answer', async () => {
    vi.mocked(client.post).mockRejectedValueOnce(refusal(409, 'retry_later', 'Let’s Encrypt refuses new attempts until 2026-09-26 10:00 UTC.', undefined, 'Playkeeper tries again by itself then.'))
    const add = vi.spyOn(toastManager, 'add')
    await show(free({ names: { url: 'https://names.playkeeper.io', unreachable: true }, certificate: { names: ['alex.playkeeper.me'], challenge: 'dns-01', problem: { code: 'dns_timeout', message: 'The challenge record didn’t show up in time.', hint: 'Try again in a few minutes.' } } }))
    expect(text()).toContain('Couldn’t get a certificate')
    expect(text()).toContain('The challenge record didn’t show up in time. Try again in a few minutes.')
    expect(text()).not.toContain('isn’t answering')
    await click(button('Try again'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/certificate', { acceptTerms: true })
    expect(add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Let’s Encrypt refuses new attempts until 2026-09-26 10:00 UTC. Playkeeper tries again by itself then.', type: 'error' }))
  })

  it('says in one quiet line when the service doesn’t answer, and the addresses keep working', async () => {
    await show(free({ names: { url: 'https://names.playkeeper.io', unreachable: true } }))
    expect(text()).toContain('alex.playkeeper.me is yours')
    expect(text()).toContain('The free address service isn’t answering right now. Your addresses keep working.')
    expect(text()).not.toContain('couldn’t reach')
    expect(document.querySelector('[role="alert"]')).toBeNull()
    expect(link('Open https://alex.playkeeper.me:8443')).toBeTruthy()
  })

  it('answers Release with the same one line while the service doesn’t answer, and keeps the name', async () => {
    const add = vi.spyOn(toastManager, 'add')
    vi.mocked(client.post).mockRejectedValueOnce(refusal(503, 'names_unreachable', 'Playkeeper couldn’t reach the free address service.', { detail: 'connect: connection refused' }, 'Addresses that already work keep working. Try again later.'))
    await show(free())
    await click(button('Release it'))
    await click(button('Release it', dialog()))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/release', {})
    expect(add).toHaveBeenCalledWith(expect.objectContaining({ title: 'The free address service isn’t answering right now. Your addresses keep working.', type: 'error' }))
    expect(add).not.toHaveBeenCalledWith(expect.objectContaining({ title: expect.stringContaining('couldn’t reach') }))
    expect(text()).toContain('alex.playkeeper.me is yours')
  })

  it('shows one notice at a time: the service not answering before the servers’ wait, whose date stays on each row', async () => {
    const serversFrom = '2026-09-28T12:00:00Z'
    await show(free({ ...waiting, names: { url: 'https://names.playkeeper.io', unreachable: true } }, { serversWait: 'server_address_not_yet', serversFrom }))
    expect(text()).toContain('The free address service isn’t answering right now.')
    expect(text()).not.toContain('Server addresses start on')
    expect(text()).toContain(`survival.alex.playkeeper.me from ${formatDate(serversFrom)}`)
  })

  it('says why it lapsed when the dashboard didn’t answer on port 8443, and a refresh that still can’t reach it says so', async () => {
    const stoppedAt = '2026-10-03T09:00:00Z'
    vi.mocked(client.post).mockRejectedValueOnce(refusal(409, 'not_answering', 'alex.playkeeper.me stays off.', { name: 'alex', ip, port: 8443 }, 'Open port 8443.'))
    const add = vi.spyOn(toastManager, 'add')
    await show(free({}, { state: 'lapsed', lapseReason: 'no_answer', stoppedAt }))
    expect(text()).toContain(`This address stopped updating on ${formatDate(stoppedAt)}`)
    expect(text()).toContain('The free address service couldn’t reach my-vps on port 8443 for a week. Open port 8443, then refresh.')
    expect(text()).not.toContain('offline for about a month')
    await click(button('Refresh'))
    expect(add).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'The free address service couldn’t reach my-vps', description: 'Open port 8443 in your VPS provider’s firewall, then try again.', type: 'error' }),
    )
  })

  const waiting = { servers: [survival({ label: 'survival', address: 'survival.alex.playkeeper.me' }), creative({ label: 'creative', address: 'creative.alex.playkeeper.me' })] }

  it('gives the servers their addresses a few days after the claim, and the name with the port meanwhile', async () => {
    const serversFrom = '2026-09-28T12:00:00Z'
    await show(free(waiting, { serversWait: 'server_address_not_yet', serversFrom }))
    expect(text()).toContain('alex.playkeeper.me is yours')
    expect(text()).toContain(`Server addresses start on ${formatDate(serversFrom)}`)
    expect(text()).toContain('Until then, players join at alex.playkeeper.me with the port listed below.')
    expect(text()).toContain(`survival.alex.playkeeper.me from ${formatDate(serversFrom)}`)
    expect(text()).toContain('alex.playkeeper.me:25566')
    expect(document.querySelector('[aria-label="Copy alex.playkeeper.me:25566"]')).toBeTruthy()
    expect(text()).not.toContain('publishing…')
  })

  it('says server addresses wait for port 8443 until the service reaches the dashboard', async () => {
    await show(free(waiting, { serversWait: 'not_answering' }))
    expect(text()).toContain('Server addresses wait for port 8443')
    expect(text()).toContain('The free address service hasn’t reached my-vps on port 8443 yet.')
    expect(text()).toContain('creative.alex.playkeeper.me once port 8443 is open')
    expect(link('How to open a port')).toBeTruthy()
  })

  it('says when the certificate limit lets it try again, without a Try again that can only fail', async () => {
    const retryAt = new Date(Date.now() + 2 * 86_400_000).toISOString()
    const limited = (kind: string, renewing: boolean) =>
      free({ certificate: { names: ['alex.playkeeper.me'], challenge: 'dns-01', notAfter: renewing ? notAfter : undefined, problem: { code: 'certificate_limit', params: { kind, until: retryAt }, message: 'Paused.', retryAt } } })
    await show(limited('all', false))
    expect(text()).toContain('New certificates are paused for now')
    expect(text()).toContain(`to stay within Let’s Encrypt’s limits. Playkeeper tries again after ${formatDateTime(retryAt)}. Players can still join.`)
    expect(buttons('Try again')).toHaveLength(0)
    await show(limited('name', true))
    expect(text()).toContain('Couldn’t renew the certificate')
    expect(text()).toContain(`It runs out on ${formatDate(notAfter)}. alex.playkeeper.me has asked for as many certificates this week as one free address can. Playkeeper tries again after ${formatDateTime(retryAt)}.`)
    expect(buttons('Try again')).toHaveLength(0)
  })
})

describe('an own domain', () => {
  it('gives a server its own address, with its two records and whether each works', async () => {
    const aAlex: DNSRecord = { serverId: 's2', type: 'A', name: 'alex.example.com', value: ip, ttl: 300 }
    const srvAlex: DNSRecord = { serverId: 's2', type: 'SRV', name: '_minecraft._tcp.alex.example.com', value: '0 5 25566 alex.example.com.', ttl: 300 }
    vi.mocked(client.post).mockResolvedValue(own())
    await show(
      own({
        servers: [survival({ address: 'play.example.com', published: true }), creative({ address: 'alex.example.com', ownAddress: 'alex.example.com' })],
        records: [aRecord, aAlex, srvAlex],
        check: check({}, true, [
          { record: srvRecord, ok: true, code: 'srv_ok', message: 'The SRV record is right.' },
          { record: aAlex, ok: true, code: 'name_ok', message: 'It points here.', own: true },
          { record: srvAlex, ok: false, code: 'srv_missing', message: 'The SRV record for alex.example.com does not exist yet.', own: true },
        ]),
      }),
    )
    // The domain works whatever a server's own address does.
    expect(text()).toContain('play.example.com is ready')
    expect(text()).not.toContain('does not exist yet')
    expect(text()).toContain('Servers’ own addresses')
    expect(field('Creative').value).toBe('alex.example.com')
    expect(field('Survival').value).toBe('')
    expect(text()).toContain(`Aalex.example.com${ip}Works`)
    expect(text()).toContain('SRV_minecraft._tcp.alex.example.com0 5 25566 alex.example.com.Not yet')
    const save = (server: string) => need(document.querySelector<HTMLButtonElement>(`[aria-label="Save ${server}’s address"]`), `Save for ${server}`)
    expect(save('Survival').disabled).toBe(true)
    await type(field('Survival'), 'Sam.Example.com')
    await click(save('Survival'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/s1/own-address', { address: 'sam.example.com' })
    await click(need(document.querySelector<HTMLButtonElement>('[aria-label="Remove Creative’s address"]'), 'Remove for Creative'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/s2/own-address', { address: '' })
  })

  it('lets the owner turn on an address for each server', async () => {
    vi.mocked(client.post).mockResolvedValue(own())
    await show(own())
    expect(text()).toContain('An address for each server')
    expect(text()).toContain('Add one record, *.play.example.com, pointing at this machine')
    const toggle = need(document.querySelector<HTMLElement>('[role="switch"]'), 'the switch')
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    await click(toggle)
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/server-addresses', { on: true })
  })

  it('gives every server an address with one wildcard record', async () => {
    const wild: DNSRecord = { type: 'A', name: '*.play.example.com', value: ip, ttl: 300 }
    await show(
      own({
        serverAddresses: true,
        servers: [
          survival({ label: 'survival', address: 'survival.play.example.com', ownAddress: 'survival.play.example.com', automatic: true, published: true }),
          creative({ label: 'creative', address: 'creative.play.example.com:25566', ownAddress: 'creative.play.example.com', automatic: true }),
        ],
        records: [aRecord, wild],
        check: check({}, true, [{ record: wild, ok: false, code: 'name_missing', message: 'No record.', own: true }]),
      }),
    )
    expect(need(document.querySelector<HTMLElement>('[role="switch"]'), 'the switch').getAttribute('aria-checked')).toBe('true')
    expect(text()).toContain(`A*.play.example.com${ip}Not yet`)
    expect(text()).toContain('Address: survival.play.example.com Works')
    expect(text()).toContain('Address: creative.play.example.com:25566 Not yet')
    // An address from the wildcard isn't one given to the server: the field
    // stays free for one of its own, with nothing to remove.
    expect(field('Creative').value).toBe('')
    expect(document.querySelector('[aria-label="Remove Creative’s address"]')).toBeNull()
  })

  it('offers an address for each server before the first server', async () => {
    vi.mocked(client.post).mockResolvedValue(own({ servers: [], serverAddresses: true }))
    await show(own({ servers: [] }))
    expect(text()).toContain('Servers’ own addresses')
    expect(text()).toContain('An address for each server')
    const toggle = need(document.querySelector<HTMLElement>('[role="switch"]'), 'the switch')
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    await click(toggle)
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/server-addresses', { on: true })
  })

  it('says whether the wildcard record works before the first server', async () => {
    const wild: DNSRecord = { type: 'A', name: '*.play.example.com', value: ip, ttl: 300 }
    await show(own({ servers: [], serverAddresses: true, records: [aRecord, wild], check: check({}, true, [{ record: wild, ok: true, code: 'name_ok', message: 'It points here.', own: true }]) }))
    expect(need(document.querySelector<HTMLElement>('[role="switch"]'), 'the switch').getAttribute('aria-checked')).toBe('true')
    expect(text()).toContain(`A*.play.example.com${ip}Works`)
  })

  it('says why a server has no address of its own under the wildcard', async () => {
    await show(own({ serverAddresses: true, servers: [survival({ label: 'survival', address: 'survival.play.example.com', ownAddress: 'survival.play.example.com', automatic: true }), creative({ address: 'play.example.com:25566' })] }))
    expect(text()).toContain('Players join it at play.example.com:25566: its name can’t be an address here.')
  })

  it('lists the records for the domain, then checks them', async () => {
    const pending = own({ check: check({ ok: false, code: 'name_missing', message: 'No record.', records: [] }, false), certificate: undefined })
    vi.mocked(client.post).mockImplementation((() => {
      current = pending
      return Promise.resolve(pending)
    }) as typeof client.post)
    await show(none)
    await click(radio('Your own domain'))
    expect(text()).toContain('Add these records at your DNS provider')
    expect(button('Check records').disabled).toBe(true)
    expect(button('Check records').title).toBe('Type your domain first.')
    await type(field('Your domain'), 'https://Play.Example.com/')
    await settle()
    expect(text()).toContain('Add these records where you manage example.com')
    const rows = [...document.querySelectorAll('tbody tr')].map((r) => r.textContent)
    expect(rows[0]).toContain(`Aplay.example.com${ip}Dashboard and Survival`)
    expect(rows[1]).toContain('SRV_minecraft._tcp.creative.play.example.com0 5 25566 play.example.comCreative, on port 25566')
    expect(document.querySelector('[aria-label="Copy 0 5 25566 play.example.com"]')).toBeTruthy()
    await click(button('Check records'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/check', { domain: 'play.example.com', acceptTerms: true })
    expect(text()).toContain('No record for play.example.com yet')
    expect(text()).toContain('New records can take up to an hour. You can leave this page.')
    expect(button('Check now')).toBeTruthy()
    expect(button('Stop using it')).toBeTruthy()
  })

  it('says where the domain points instead, and Check again checks', async () => {
    vi.mocked(client.post).mockResolvedValue(own())
    await show(own({ check: check({ ok: false, code: 'name_elsewhere', message: 'Elsewhere.', params: { found: '203.0.113.7', ipv4: ip }, records: [{ type: 'A', addr: '203.0.113.7', here: false }] }, false), certificate: undefined }))
    expect(text()).toContain('play.example.com points somewhere else')
    expect(text()).toContain(`It points to 203.0.113.7. Change the A record to ${ip}.`)
    await click(button('Check again'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/check', { domain: 'play.example.com', acceptTerms: true })
  })

  it('shows the certificate being got once the domain points here', async () => {
    await show(own({ certificate: undefined, operation: op({ kind: 'certificate.issue', phase: 'certificate', startedAt: ago(20_000) }) }))
    expect(text()).toContain('play.example.com points to this machine')
    expect(text()).toContain('Next, Playkeeper gets the certificate.')
    expect(text()).toContain(`${ip} · checked just now`)
    expect(text()).toContain('Getting a certificate from Let’s Encrypt')
    expect(text()).toContain('Usually under a minute.')
    expect(text()).toMatch(/Started 2\d s ago/)
  })

  it('says to open port 80 when Let’s Encrypt couldn’t reach it', async () => {
    vi.mocked(client.post).mockResolvedValue(own())
    await show(own({ certificate: { names: ['play.example.com'], challenge: 'http-01', problem: { code: 'port80_unreachable', message: 'Let’s Encrypt couldn’t reach port 80.', hint: 'Open it.' } } }))
    expect(text()).toContain('Couldn’t get a certificate')
    expect(text()).toContain('Open port 80 in your VPS provider’s firewall, then try again.')
    expect(link('How to open a port').getAttribute('href')).toBe('https://playkeeper.io/docs/troubleshooting#friends-cant-join')
    await click(button('Try again'))
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/address/certificate', { acceptTerms: true })
  })

  it('shows until when the certificate is active while a server record is still wrong', async () => {
    await show(own({ check: check({}, false, [{ record: srvRecord, ok: false, code: 'srv_missing', message: 'No SRV record for creative.play.example.com yet.', hint: 'Add it, then check again.' }]) }))
    expect(text()).toContain(`Certificate active until ${formatLongDate(notAfter)}`)
    expect(text()).toContain('Renews by itself.')
    expect(link('Open https://play.example.com:8443').getAttribute('href')).toBe('https://play.example.com:8443')
    expect(text()).toContain('No SRV record for creative.play.example.com yet.')
    expect(text()).not.toContain('Next, Playkeeper gets the certificate.')
  })

  it('is ready once everything checks out, and asks before it stops', async () => {
    const add = vi.spyOn(toastManager, 'add')
    vi.mocked(client.del).mockImplementation((() => {
      current = none
      return Promise.resolve(none)
    }) as typeof client.del)
    await show(own())
    expect(text()).toContain('play.example.com is ready')
    for (const value of ['play.example.com', 'creative.play.example.com', 'https://play.example.com:8443']) {
      expect(document.querySelector(`[aria-label="Copy ${value}"]`), value).toBeTruthy()
    }
    await click(button('Stop using it'))
    expect(dialog().textContent).toContain('Stop using play.example.com?')
    expect(dialog().textContent).toContain(`The addresses stop working right away. ${ip} keeps working.`)
    await click(button('Stop using it', dialog()))
    expect(client.del).toHaveBeenCalledWith('/api/machines/m1/address')
    expect(add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Stopped using play.example.com', type: 'success' }))
    expect(text()).toContain('Pick a name')
  })

  it('keeps the done view when a renewal fails, and says when it runs out', async () => {
    await show(own({ certificate: { names: ['play.example.com'], challenge: 'http-01', notAfter, problem: { code: 'port80_unreachable', message: 'No port 80.' } } }))
    expect(text()).toContain('play.example.com is ready')
    expect(text()).toContain('Couldn’t renew the certificate')
    expect(text()).toContain(`It runs out on ${formatDate(notAfter)}. Open port 80 while it renews, then try again.`)
    expect(text()).not.toContain('renews by itself')
  })

  it('changes the domain from the done view, and Cancel goes back', async () => {
    await show(own())
    await click(button('Change domain'))
    expect(field('Your domain').value).toBe('play.example.com')
    expect(text()).toContain('Add these records where you manage example.com')
    await click(button('Cancel'))
    expect(text()).toContain('play.example.com is ready')
  })

  describe('addresses without a port', () => {
    const answers = (over: Partial<DNSAnswers> = {}): DNSAnswers => ({
      on: false,
      zone: 'play.example.com',
      nameserver: 'ns-play.example.com',
      add: [
        { type: 'A', name: 'ns-play.example.com', value: ip, ttl: 300 },
        { type: 'NS', name: 'play.example.com', value: 'ns-play.example.com', ttl: 3600 },
      ],
      remove: [
        { type: 'A', name: 'play.example.com', value: ip, ttl: 300 },
        { type: 'A', name: '*.play.example.com', value: ip, ttl: 300 },
      ],
      servers: 0,
      listening: [],
      ...over,
    })
    const each = () => own({ serverAddresses: true, servers: [survival({ label: 'survival', address: 'survival.play.example.com:25565', ownAddress: 'survival.play.example.com', automatic: true })] })

    it('lets the owner turn them on, then shows the records to add at the parent and to remove there', async () => {
      dns = answers()
      await show(each())
      expect(text()).toContain('Addresses without a port')
      expect(text()).toContain('This machine answers DNS for play.example.com itself')
      expect(text()).not.toContain('ns-play.example.com')
      vi.mocked(client.put).mockImplementation((async () => {
        dns = answers({ on: true, answering: 'play.example.com', servers: 2, listening: [`${ip}:53`] })
        return dns
      }) as typeof client.put)
      await click(switchIn('Addresses without a port'))
      expect(client.put).toHaveBeenCalledWith('/api/dns-answers', { on: true })
      expect(text()).toContain('Add these two records at example.com:')
      expect(text()).toContain(`Ans-play.example.com${ip}`)
      expect(text()).toContain('NSplay.example.comns-play.example.com')
      expect(text()).toContain('Then remove these there')
      expect(text()).toContain(`A*.play.example.com${ip}`)
      expect(text()).toContain('Open port 53 to everyone, over UDP and TCP')
      expect(text()).toContain(`Answering on ${ip}:53, for 2 servers.`)
    })

    it('asks before it stops answering', async () => {
      dns = answers({ on: true, answering: 'play.example.com', servers: 1, listening: [`${ip}:53`] })
      await show(each())
      expect(text()).toContain(`Answering on ${ip}:53, for 1 server.`)
      await click(switchIn('Addresses without a port'))
      expect(client.put).not.toHaveBeenCalled()
      expect(dialog().textContent).toContain('Stop answering DNS for play.example.com?')
      expect(dialog().textContent).toContain('Put back the records you removed at example.com first.')
      vi.mocked(client.put).mockResolvedValue(answers())
      await click(button('Stop answering', dialog()))
      expect(client.put).toHaveBeenCalledWith('/api/dns-answers', { on: false })
    })

    it('says why a domain with nothing above it that could hand it over can’t be answered', async () => {
      dns = answers({ zone: 'example.com', nameserver: undefined, unavailable: 'subdomain', add: [], remove: [] })
      await show(each())
      expect(text()).toContain('Only a name under your domain can be answered here, like beta.example.com')
      expect(switchIn('Addresses without a port').getAttribute('aria-disabled')).toBe('true')
    })

    it('is only on the dashboard’s own machine', async () => {
      dns = answers()
      await show(each(), { kind: 'remote' })
      expect(text()).toContain('An address for each server')
      expect(text()).not.toContain('Addresses without a port')
    })
  })
})

describe('on a phone', () => {
  it('lists the choice, the name and the addresses, with Claim at the bottom', async () => {
    await show(none, { phone: true })
    expect(text()).toContain('How people reach my-vps')
    expect(text()).toContain('Ready in a minute')
    expect(text()).toContain('You add a record or two')
    await settle()
    expect(text()).toContain('siya.playkeeper.me is free')
    expect(text()).toContain('survival.siya.playkeeper.me')
    expect(button('Claim siya.playkeeper.me').disabled).toBe(false)
    await click(radio('Your own domain'))
    expect(text()).not.toContain('Ready in a minute')
  })

  it('opens the dashboard without https:// in the label, and copies each address', async () => {
    await show(free(), { phone: true })
    expect(link('Open alex.playkeeper.me:8443').getAttribute('href')).toBe('https://alex.playkeeper.me:8443')
    expect(document.querySelector('[aria-label="Copy survival.alex.playkeeper.me"]')?.textContent).toBe('')
    expect(button('Change name')).toBeTruthy()
    expect(button('Release it')).toBeTruthy()
  })

  it('groups each record by what it serves', async () => {
    await show(own({ check: check({ ok: false, code: 'name_missing', message: 'No record.', records: [] }, false), certificate: undefined }), { phone: true })
    expect(text()).toContain('A record · Dashboard and Survival')
    expect(text()).toContain('SRV record · Creative')
    expect(button('Check again')).toBeTruthy()
  })

  it('keeps servers’ own addresses and their switch in list items', async () => {
    await show(own({ serverAddresses: true, servers: [survival({ label: 'survival', address: 'survival.play.example.com', ownAddress: 'survival.play.example.com', automatic: true }), creative({ address: 'play.example.com:25566' })] }), { phone: true })
    expect(text()).toContain('Servers’ own addresses')
    expect(text()).toContain('An address for each server')
    expect(strayListChildren()).toEqual([])
  })
})
