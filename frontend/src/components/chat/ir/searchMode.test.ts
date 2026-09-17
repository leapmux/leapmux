import { describe, expect, it } from 'vitest'
import { SEARCH_MODES, searchMode } from './searchMode'

describe('searchMode', () => {
  it.each(SEARCH_MODES)('keeps the declared mode %s', (mode) => {
    expect(searchMode(mode)).toBe(mode)
  })

  it('refuses a word no release declared', () => {
    // The renderer compares against `count`; a near miss must not reach it as a mode.
    expect(searchMode('counts')).toBeUndefined()
    expect(searchMode('Count')).toBeUndefined()
    expect(searchMode('')).toBeUndefined()
  })

  it('refuses a value that is not text', () => {
    expect(searchMode(undefined)).toBeUndefined()
    expect(searchMode(null)).toBeUndefined()
    expect(searchMode(3)).toBeUndefined()
    expect(searchMode(['count'])).toBeUndefined()
  })
})
