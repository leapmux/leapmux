import { describe, expect, it } from 'vitest'
import { countLines, hasMoreLinesThan } from './collapse'

describe('hasMoreLinesThan', () => {
  it('counts newlines and short-circuits at the threshold', () => {
    expect(hasMoreLinesThan('a\nb\nc', 3)).toBe(false) // 3 lines, not MORE than 3
    expect(hasMoreLinesThan('a\nb\nc\nd', 3)).toBe(true) // 4 lines
    expect(hasMoreLinesThan('', 1000)).toBe(false) // empty body is well under the cap
  })
})

// It replaced `content.replace(/\n$/, '').split('\n').length`, which copies the whole
// body and allocates one string per line -- on every reactive pass of a write title.
// The two must agree on every boundary, because the number is what the row STATES.
describe('countLines', () => {
  it.each([
    ['', 0],
    ['\n', 1],
    ['one', 1],
    ['one\n', 1],
    ['one\ntwo', 2],
    ['one\ntwo\n', 2],
    ['\n\n', 2],
    ['one\n\nthree', 3],
  ])('counts %j as %i lines', (text, expected) => {
    expect(countLines(text)).toBe(expected)
    // The expression it replaced, as the oracle.
    expect(countLines(text)).toBe(text === '' ? 0 : text.replace(/\n$/, '').split('\n').length)
  })
})
