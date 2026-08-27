import type { SettingDescriptor } from './types'
import { describe, expect, it } from 'vitest'
import { descriptorNeedsElevation, descriptorVisible } from './types'

function descriptor(overrides: Partial<SettingDescriptor> = {}): SettingDescriptor {
  return {
    id: 'test.row',
    category: 'appearance',
    label: 'Test row',
    scope: 'browser',
    control: { kind: 'toggle' },
    ...overrides,
  }
}

describe('descriptorVisible', () => {
  it('shows a row with no hidden predicate', () => {
    expect(descriptorVisible(descriptor())).toBe(true)
  })

  it('reads the predicate on every call, so a live answer applies', () => {
    let hide = false
    const d = descriptor({ hidden: () => hide })
    expect(descriptorVisible(d)).toBe(true)
    hide = true
    expect(descriptorVisible(d)).toBe(false)
  })
})

/**
 * The scope answers for the hub tier; the flag carries the account rows.
 *
 * Every generated hub row used to set `needsElevation: true` three lines under
 * the `scope: 'hub'` that already implies it. The hub requires the same window
 * for every settings write rather than for one key at a time, so the scope IS
 * the answer -- and a hub key added later gets that answer from its scope.
 */
describe('descriptorNeedsElevation', () => {
  it('answers yes for every hub row, with no flag of its own', () => {
    expect(descriptorNeedsElevation(descriptor({ scope: 'hub' }))).toBe(true)
  })

  // The four account rows the scope cannot speak for: the password, the
  // passkeys, the email address and the linked providers.
  it('keeps the explicit flag for an account row', () => {
    expect(descriptorNeedsElevation(descriptor({ scope: 'account', needsElevation: true }))).toBe(true)
  })

  // And the two account rows that genuinely do not need one: the profile name
  // and the command-line credentials.
  it('answers no for an account row that declares nothing', () => {
    expect(descriptorNeedsElevation(descriptor({ scope: 'account' }))).toBe(false)
  })

  it('answers no for a browser row', () => {
    expect(descriptorNeedsElevation(descriptor({ scope: 'browser' }))).toBe(false)
    expect(descriptorNeedsElevation(descriptor({ scope: 'dual' }))).toBe(false)
  })
})
