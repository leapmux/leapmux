/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { sameKeys } from './sameKeys'

describe('sameKeys', () => {
  it('treats the same reference as equal without walking it', () => {
    const a = new Set(['x'])
    expect(sameKeys(a, a)).toBe(true)
  })

  it('ignores insertion order', () => {
    expect(sameKeys(new Set(['a', 'b']), new Set(['b', 'a']))).toBe(true)
  })

  it('separates sets of the same size with different members', () => {
    expect(sameKeys(new Set(['a', 'b']), new Set(['a', 'c']))).toBe(false)
  })

  it('separates sets of different sizes', () => {
    expect(sameKeys(new Set(['a']), new Set(['a', 'b']))).toBe(false)
  })

  it('compares a Map on its keys, not its values', () => {
    const a = new Map([['k', 1]])
    const b = new Map([['k', 2]])
    expect(sameKeys(a, b), 'membership is the question; values are not').toBe(true)
  })

  // The two memos this replaced compared `ids.sort().join(' ')` and
  // `[...keys].sort().join('\u0000')`. A separator appearing inside a member
  // makes two DIFFERENT sets produce the same string, so the memo reports "no
  // change" and its effect never re-runs — a hydration candidate set that
  // silently stops being dispatched.
  it('distinguishes sets a sorted-join would collapse', () => {
    const a = new Set(['a b', 'c'])
    const b = new Set(['a', 'b c'])
    expect([...a].sort().join(' '), 'the join really does collide')
      .toBe([...b].sort().join(' '))
    expect(sameKeys(a, b)).toBe(false)
  })

  it('handles null and undefined on either side', () => {
    expect(sameKeys(null, null)).toBe(true)
    expect(sameKeys(undefined, undefined)).toBe(true)
    expect(sameKeys(null, new Set(['a']))).toBe(false)
    expect(sameKeys(new Set(['a']), undefined)).toBe(false)
  })

  it('treats two empty sets as equal', () => {
    expect(sameKeys(new Set(), new Set())).toBe(true)
  })
})
