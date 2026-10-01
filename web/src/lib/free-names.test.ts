import { describe, expect, it } from 'vitest'
import { nameProblem } from './address'
import { friendlyName } from './free-names'

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
})
