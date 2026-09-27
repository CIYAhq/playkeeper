import { describe, expect, it } from 'vitest'
import { catalogPath } from './catalog'

describe('catalogPath', () => {
  it.each([
    { name: 'a type', opts: { type: 'paper' }, path: '/api/machines/m1/catalog?type=paper' },
    { name: 'a pack, whose mods size its memory', opts: { type: 'neoforge', mods: 180 }, path: '/api/machines/m1/catalog?type=neoforge&mods=180' },
    { name: 'a pack that brings no mods', opts: { type: 'fabric', mods: 0 }, path: '/api/machines/m1/catalog?type=fabric&mods=0' },
    { name: 'a Paper template, whose plugins size its memory', opts: { type: 'paper', plugins: 30 }, path: '/api/machines/m1/catalog?type=paper&plugins=30' },
    { name: 'an existing server', opts: { server: 'abcdefghjk' }, path: '/api/machines/m1/catalog?server=abcdefghjk' },
  ])('asks for $name', ({ opts, path }) => {
    expect(catalogPath('m1', opts)).toBe(path)
  })
})
