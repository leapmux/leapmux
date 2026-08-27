import { describe, expect, it } from 'vitest'
import { stringParam } from './searchParam'

describe('stringParam', () => {
  it('returns the value when the parameter appears once', () => {
    expect(stringParam('/workspace/a')).toBe('/workspace/a')
  })

  // A repeated name is what forces the guard: ?redirect=/a&redirect=/b
  // arrives as an array, and picking either one acts on a URL the user did
  // not write.
  it('returns undefined for a repeated parameter', () => {
    expect(stringParam(['/a', '/b'])).toBeUndefined()
  })

  it('returns undefined for an absent parameter', () => {
    expect(stringParam(undefined)).toBeUndefined()
  })

  // An empty value is PRESENT, not absent: the caller decides with `??`.
  // This is what keeps `stringParam(v) ?? ''` equivalent at the sites that
  // fall back to an empty string.
  it('keeps an empty string rather than reporting it absent', () => {
    expect(stringParam('')).toBe('')
  })
})
