import { describe, expect, it } from 'vitest'
import { jsonStringValues } from './jsonStringValues'

describe('jsonStringValues', () => {
  it('collects nested string values in document order without object keys', () => {
    expect(jsonStringValues({ KEY_ONLY: 'first', nested: [{ second: 'second' }, 'third', { deeper: { fourth: 'fourth' } }] }))
      .toEqual(['first', 'second', 'third', 'fourth'])
  })

  it('keeps an empty string and Unicode text', () => {
    expect(jsonStringValues(['', '실제 내용 🧪'])).toEqual(['', '실제 내용 🧪'])
  })

  it.each([undefined, null, 0, -1, false, true, {}, [], [0, null, false]])('returns no string for a value without text: %j', (value) => {
    expect(jsonStringValues(value)).toEqual([])
  })
})
