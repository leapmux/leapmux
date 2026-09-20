import { describe, expect, it } from 'vitest'
import { SEARCH_OUTPUT_MODES, searchOutputMode } from './searchOutputMode'

describe('searchOutputMode', () => {
  it.each(SEARCH_OUTPUT_MODES)('keeps the declared mode %s', (mode) => {
    expect(searchOutputMode(mode)).toBe(mode)
  })

  it('refuses a word no release declared', () => {
    // The renderer compares against `count`; a near miss must not reach it as a mode.
    expect(searchOutputMode('counts')).toBeUndefined()
    expect(searchOutputMode('Count')).toBeUndefined()
    expect(searchOutputMode('')).toBeUndefined()
  })

  it('refuses a value that is not text', () => {
    expect(searchOutputMode(undefined)).toBeUndefined()
    expect(searchOutputMode(null)).toBeUndefined()
    expect(searchOutputMode(3)).toBeUndefined()
    expect(searchOutputMode(['count'])).toBeUndefined()
  })
})
