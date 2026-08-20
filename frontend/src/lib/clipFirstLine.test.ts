import { describe, expect, it } from 'vitest'
import { clipFirstLine } from './clipFirstLine'

/**
 * A lone surrogate: half of an astral character, left behind by a cut that
 * landed between the pair. `String.prototype.isWellFormed` says the same thing
 * but needs an ES2024 lib target.
 */
const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/

describe('clipFirstLine', () => {
  it('returns a short line unchanged and adds no ellipsis', () => {
    expect(clipFirstLine('Explore the parser', 80)).toBe('Explore the parser')
  })

  it('keeps the first line only', () => {
    expect(clipFirstLine('first line\nsecond line', 80)).toBe('first line')
  })

  // A CRLF payload left the carriage return on the end of the line, where it
  // renders as nothing and still counts toward the length.
  it('drops a trailing carriage return from a CRLF line', () => {
    expect(clipFirstLine('first line\r\nsecond line', 80)).toBe('first line')
  })

  it('clips at the limit and marks the cut', () => {
    expect(clipFirstLine('x'.repeat(200), 10)).toBe(`${'x'.repeat(10)}…`)
  })

  // The defect this helper exists to remove. A raw slice at a fixed offset lands
  // between the two halves of a surrogate pair, and the lone surrogate left
  // behind renders as a replacement glyph.
  it('never cuts an astral character in half', () => {
    const text = `${'x'.repeat(9)}😀 and more text after it`
    const clipped = clipFirstLine(text, 10)
    expect(LONE_SURROGATE.test(clipped)).toBe(false)
    expect(clipped).toBe(`${'x'.repeat(9)}😀…`)
  })

  it('reports blank input as an empty string', () => {
    expect(clipFirstLine('   \n\t ', 80)).toBe('')
    expect(clipFirstLine('', 80)).toBe('')
  })
})
