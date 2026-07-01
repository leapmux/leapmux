import { describe, expect, it } from 'vitest'
import { canHighlightBySize, hasMoreLinesThan, HIGHLIGHT_CHAR_LIMIT, HIGHLIGHT_LINE_LIMIT } from './collapse'

describe('hasMoreLinesThan', () => {
  it('counts newlines and short-circuits at the threshold', () => {
    expect(hasMoreLinesThan('a\nb\nc', 3)).toBe(false) // 3 lines, not MORE than 3
    expect(hasMoreLinesThan('a\nb\nc\nd', 3)).toBe(true) // 4 lines
    expect(hasMoreLinesThan('', 1000)).toBe(false) // empty body is well under the cap
  })
})

describe('canHighlightBySize', () => {
  it('accepts a body within both default caps', () => {
    expect(canHighlightBySize('echo hi')).toBe(true)
    expect(canHighlightBySize('')).toBe(true)
  })

  it('rejects a body over the default char cap', () => {
    // One char past HIGHLIGHT_CHAR_LIMIT, single line: char cap is the deciding factor.
    expect(canHighlightBySize('x'.repeat(HIGHLIGHT_CHAR_LIMIT + 1))).toBe(false)
    // Exactly at the cap is still allowed (<=).
    expect(canHighlightBySize('x'.repeat(HIGHLIGHT_CHAR_LIMIT))).toBe(true)
  })

  it('rejects a body over the default line cap even when under the char cap', () => {
    // HIGHLIGHT_LINE_LIMIT + 1 lines (each a single char) -> well under the char cap.
    const overLines = Array.from<string>({ length: HIGHLIGHT_LINE_LIMIT + 2 }).fill('a').join('\n')
    expect(overLines.length).toBeLessThan(HIGHLIGHT_CHAR_LIMIT)
    expect(canHighlightBySize(overLines)).toBe(false)
  })

  it('honors per-surface overrides (the command-input char cap)', () => {
    // The command summary passes a tighter char cap; a body over it is ineligible
    // even though it is under the default cap.
    expect(canHighlightBySize('x'.repeat(1001), { maxChars: 1000 })).toBe(false)
    expect(canHighlightBySize('x'.repeat(1000), { maxChars: 1000 })).toBe(true)
  })
})
