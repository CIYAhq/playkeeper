import { describe, expect, it } from 'vitest'
import { nameProblem } from './address'
import { friendlyName, seeded } from './free-names'

describe('friendly free names', () => {
  it('are an adjective, an animal and a number, within the names service’s rule', () => {
    expect(friendlyName(() => 0)).toBe('brave-otter-10')
    expect(friendlyName(() => 0.999)).toBe('mossy-yak-99')
    for (let i = 0; i < 500; i++) {
      const name = friendlyName()
      expect(name).toMatch(/^[a-z]+-[a-z]+-[1-9]\d$/)
      expect(nameProblem(name), name).toBeUndefined()
      expect(name).not.toContain('playkeeper')
    }
  })

  // A page offers the same name each time it opens, which the click-through
  // also needs to find a control again.
  it('are the same for the same seed, and differ between seeds', () => {
    expect(friendlyName(seeded('m2345abcde\n0\n0'))).toBe(friendlyName(seeded('m2345abcde\n0\n0')))
    const names = new Set(Array.from({ length: 20 }, (_, i) => friendlyName(seeded(`m2345abcde\n${i}\n0`))))
    expect(names.size).toBeGreaterThan(15)
  })
})
