import { describe, expect, it } from 'vitest'
import { __setGraphemeSegmenterForTest, truncatePreview } from './textTruncate'

describe('truncate preview', () => {
  it('tidies horizontal whitespace but preserves newlines (for markdown structure)', () => {
    expect(truncatePreview('  a\n\n  b\t c  ')).toBe('a\n\nb c')
    expect(truncatePreview('one\n\n\n\ntwo')).toBe('one\n\ntwo') // blank-line runs cap at one
  })

  it('preserves bare \\r (classic-Mac) and \\r\\n line endings as newlines, not spaces', () => {
    expect(truncatePreview('a\rb')).toBe('a\nb') // a bare CR is a line break, not a space
    expect(truncatePreview('a\r\nb')).toBe('a\nb') // a CRLF grapheme is one newline
  })

  it('coalesces CRLF to a single newline on the no-Intl.Segmenter fallback path (not a paragraph break)', () => {
    // Force the code-point fallback (older engines / SSR). Without CRLF coalescing there, the
    // '\r' and '\n' each hit the newline branch and one CRLF double-counts into a paragraph
    // break, diverging from the Segmenter path asserted above.
    __setGraphemeSegmenterForTest(null)
    try {
      expect(truncatePreview('a\r\nb')).toBe('a\nb')
      expect(truncatePreview('a\rb')).toBe('a\nb') // a bare CR still one newline on the fallback
    }
    finally {
      __setGraphemeSegmenterForTest(undefined) // re-resolve the real segmenter for later tests
    }
  })

  it('bounds the graphemes scanned on a whitespace-dominated input (does not segment the whole prefix)', () => {
    // Whitespace graphemes never append, so the content cap can't stop the loop; the scan cap
    // must. A counting segmenter proves the loop stops near MAX_PREVIEW_SCAN, not at 1_000_004.
    let pulled = 0
    __setGraphemeSegmenterForTest({
      segment: (input: string) => ({
        * [Symbol.iterator]() {
          for (const ch of input) {
            pulled++
            yield { segment: ch }
          }
        },
      }),
    })
    try {
      truncatePreview(`${' '.repeat(1_000_000)}tail`)
      expect(pulled).toBeLessThan(5000) // ~MAX_PREVIEW_SCAN (4000), not the full 1_000_004
    }
    finally {
      __setGraphemeSegmenterForTest(undefined)
    }
  })

  it('preserves content that follows a whitespace run within the scan bound', () => {
    // Guards the scan bound against over-truncation: a moderate leading-whitespace run must not
    // drop the content that follows it.
    expect(truncatePreview(`${' '.repeat(50)}hello`)).toBe('hello')
  })

  it('marks the result truncated (trailing ellipsis) when the scan cap drops trailing content', () => {
    // Content, then a huge whitespace run that exhausts the scan cap BEFORE the trailing "world"
    // is reached -- so "world" is dropped. The result must carry the ellipsis affordance, not
    // silently render "hello" as if it were the whole message.
    expect(truncatePreview(`hello${' '.repeat(5000)}world`)).toBe('hello…')
  })

  it('returns null for empty / whitespace-only / nullish input', () => {
    expect(truncatePreview('')).toBeNull()
    expect(truncatePreview('   \n ')).toBeNull()
    expect(truncatePreview(undefined)).toBeNull()
    expect(truncatePreview(null)).toBeNull()
  })

  it('caps overlong text at the limit with a trailing ellipsis', () => {
    const out = truncatePreview('x'.repeat(500))!
    expect(out.endsWith('…')).toBe(true)
    expect(out.length).toBe(201) // 200 kept + the ellipsis
  })

  it('does not split a surrogate pair at the truncation boundary', () => {
    expect(truncatePreview(`${'x'.repeat(199)}😀tail`)).toBe(`${'x'.repeat(199)}😀…`)
  })

  it('does not split a combining-character grapheme at the truncation boundary', () => {
    expect(truncatePreview(`${'x'.repeat(199)}e\u0301tail`)).toBe(`${'x'.repeat(199)}e\u0301…`)
  })
})
