import { describe, expect, it } from 'vitest'
import type { PagePort, PublicPageView, PublicServer } from '@/api/types'
import { joinSteps, pageReach, portProblem, publicStatus, serverPageFace, serverPageIcon, serverPageTitle, streamEmbed, untilText } from './server-page'

function server(over: Partial<PublicServer> = {}): PublicServer {
  return { slug: 'survival', name: 'Survival', motd: 'Hi', address: 'alex.playkeeper.me', state: 'online', minecraftVersion: '1.21.10', type: 'paper', inviteOnly: false, hasIcon: false, ...over }
}

const open = (port: number): PagePort => ({ port, state: 'open' })
const off = (port: number): PagePort => ({ port, state: 'off' })

function view(over: Partial<PublicPageView> = {}): PublicPageView {
  return { enabled: true, players: false, about: '', stream: '', host: 'alex.playkeeper.me', ports: { https: open(443), http: open(80) }, ...over }
}

describe('the public server page', () => {
  it('says how the server is doing without naming anyone', () => {
    expect(publicStatus(server({ players: { online: 3, max: 10 } }))).toEqual({ tone: 'online', label: 'Online', detail: '3 of 10 playing' })
    expect(publicStatus(server({ players: { online: 0, max: 10 } })).detail).toBe('Nobody’s playing right now')
    expect(publicStatus(server({ state: 'sleeping' }))).toEqual({ tone: 'stopped', label: 'Asleep', detail: 'Joining wakes it up' })
    expect(publicStatus(server({ state: 'starting' })).tone).toBe('busy')
    expect(publicStatus(server({ state: 'offline', players: { online: 3, max: 10 } }))).toEqual({ tone: 'stopped', label: 'Offline' })
  })

  it('gives two lines on joining, the modpack first when there is one, and a third when only invited players get in', () => {
    expect(joinSteps(server())).toEqual(['Open Minecraft: Java Edition 1.21.10 and choose Multiplayer, then Add Server.', 'Paste the address and join.'])
    expect(joinSteps(server({ modpack: { name: 'All the Mods 10', version: '4.2' } }))[0]).toBe('Install the All the Mods 10 modpack, open it and choose Multiplayer, then Add Server.')
    expect(joinSteps(server({ inviteOnly: true }))).toHaveLength(3)
  })

  it('names the tab after the server, or the address for several', () => {
    expect(serverPageTitle('alex.playkeeper.me', [server()])).toBe('Survival · Minecraft server')
    expect(serverPageTitle('alex.playkeeper.me', [server(), server({ slug: 'creative' })])).toBe('alex.playkeeper.me · Minecraft servers')
  })

  it('asks for icons and faces on the page’s own address only', () => {
    expect(serverPageIcon(server())).toBeUndefined()
    expect(serverPageIcon(server({ hasIcon: true, slug: 'my-world' }))).toBe('/api/public/server-page/icons/my-world')
    expect(serverPageFace('mara_k')).toBe('/api/public/server-page/faces/mara_k')
  })
})

describe('whether browsers reach the page, for Settings', () => {
  it('links the page once port 443 serves it, on its port when that isn’t 443', () => {
    expect(pageReach(view())).toEqual({ kind: 'live', url: 'https://alex.playkeeper.me' })
    expect(pageReach(view({ ports: { https: open(8444), http: open(8480) } }))).toEqual({ kind: 'live', url: 'https://alex.playkeeper.me:8444' })
  })

  it('links plain HTTP when only port 80 serves it, and says why', () => {
    const https: PagePort = { port: 443, state: 'busy', holder: 'nginx' }
    expect(pageReach(view({ ports: { https, http: open(80) } }))).toEqual({ kind: 'plainOnly', url: 'http://alex.playkeeper.me', https })
    expect(portProblem(https)).toBe('nginx uses port 443, so Playkeeper leaves it alone.')
  })

  it('says what holds the page back', () => {
    expect(pageReach(view({ enabled: false })).kind).toBe('off')
    expect(pageReach(view({ ports: undefined })).kind).toBe('noAddress')
    expect(pageReach(view({ host: undefined })).kind).toBe('noAddress')
    expect(pageReach(view({ ports: { https: off(443), http: off(80) } })).kind).toBe('opening')
    expect(pageReach(view({ ports: { https: { port: 443, state: 'waiting' }, http: { port: 80, state: 'waiting' } } })).kind).toBe('waiting')
    const claimed: PagePort = { port: 443, state: 'claimed', holder: 'traefik' }
    expect(pageReach(view({ ports: { https: claimed, http: { port: 80, state: 'claimed', holder: 'traefik' } } }))).toEqual({ kind: 'blocked', port: claimed })
    expect(portProblem(claimed)).toBe('traefik is set to use port 443, so Playkeeper leaves it alone.')
    expect(portProblem({ port: 443, state: 'busy' })).toBe('Another program uses port 443, so Playkeeper leaves it alone.')
    expect(portProblem({ port: 80, state: 'denied' })).toBe('This machine doesn’t let Playkeeper use port 80.')
  })

  it('links plain HTTP while port 443 has no certificate for a joined server’s address, and says so', () => {
    const https: PagePort = { port: 443, state: 'no_certificate' }
    expect(pageReach(view({ host: 'steve.beta.playkeeper.me', ports: { https, http: open(80) } }))).toEqual({ kind: 'plainOnly', url: 'http://steve.beta.playkeeper.me', https })
    expect(portProblem(https)).toBe('Port 443 has no certificate for this address yet, so browsers get the page over HTTP.')
  })
})

describe('the owner’s stream and its countdown', () => {
  it('loads the stream’s own player, told which page it plays on', () => {
    expect(streamEmbed({ site: 'twitch', channel: 'example_channel', url: 'https://www.twitch.tv/example_channel' }, 'ai.playkeeper.me')).toBe(
      'https://player.twitch.tv/?channel=example_channel&parent=ai.playkeeper.me&autoplay=true',
    )
    expect(streamEmbed({ site: 'youtube', channel: 'UCabcdefghijklmnopqrstuv', url: 'https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv' }, 'ai.playkeeper.me')).toBe(
      'https://www.youtube-nocookie.com/embed/live_stream?channel=UCabcdefghijklmnopqrstuv&autoplay=1',
    )
  })

  it('counts down in days, hours and minutes', () => {
    const now = Date.parse('2026-10-03T12:00:00Z')
    const at = (minutes: number) => new Date(now + minutes * 60_000).toISOString()
    expect(untilText(at(3 * 60 + 12), now)).toBe('3 h 12 min')
    expect(untilText(at(12), now)).toBe('12 min')
    expect(untilText(at(2 * 24 * 60 + 4 * 60), now)).toBe('2 days 4 h')
    expect(untilText(at(24 * 60 + 30), now)).toBe('1 day 0 h')
    expect(untilText(at(0), now)).toBe('a moment')
    expect(untilText(at(-5), now)).toBe('a moment')
  })
})
