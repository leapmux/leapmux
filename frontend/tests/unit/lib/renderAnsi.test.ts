import { afterEach, describe, expect, it, vi } from 'vitest'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { shikiHighlighter } from '~/lib/renderMarkdown'

const SPAN_TAG_RE = /<span/g

describe('containsAnsi', () => {
  it('returns false for plain text', () => {
    expect(containsAnsi('hello world')).toBe(false)
  })

  it('returns false for empty string', () => {
    expect(containsAnsi('')).toBe(false)
  })

  it('detects basic color codes', () => {
    expect(containsAnsi('\x1B[31mred\x1B[0m')).toBe(true)
  })

  it('detects reset code alone', () => {
    expect(containsAnsi('\x1B[0m')).toBe(true)
  })

  it('detects 256-color codes', () => {
    expect(containsAnsi('\x1B[38;5;196mred\x1B[0m')).toBe(true)
  })

  it('detects true color (24-bit) codes', () => {
    expect(containsAnsi('\x1B[38;2;255;0;0mred\x1B[0m')).toBe(true)
  })

  it('detects bold code', () => {
    expect(containsAnsi('\x1B[1mbold\x1B[0m')).toBe(true)
  })

  it('detects underline code', () => {
    expect(containsAnsi('\x1B[4munderline\x1B[0m')).toBe(true)
  })

  it('detects non-SGR CSI escapes that fallback rendering strips', () => {
    expect(containsAnsi('\x1B[H')).toBe(true)
    expect(containsAnsi('\x1B[2K')).toBe(true)
  })

  it('detects OSC escapes that fallback rendering strips', () => {
    expect(containsAnsi('\x1B]8;;https://example.com\x1B\\link\x1B]8;;\x1B\\')).toBe(true)
  })
})

describe('renderAnsi', () => {
  it('produces HTML with shiki class', () => {
    const html = renderAnsi('\x1B[31mred text\x1B[0m')
    expect(html).toContain('class="shiki')
    expect(html).toContain('red text')
  })

  it('produces CSS variable-based styles for colored text', () => {
    const html = renderAnsi('\x1B[31mred\x1B[0m')
    expect(html).toContain('--shiki-light:')
    expect(html).toContain('--shiki-dark:')
  })

  it('renders plain text without ANSI codes', () => {
    const html = renderAnsi('plain text')
    expect(html).toContain('plain text')
    expect(html).toContain('<pre')
  })

  it('escapes HTML characters in content', () => {
    const html = renderAnsi('<script>alert("xss")</script>')
    expect(html).not.toContain('<script>')
    // Shiki uses XML numeric entities (&#x3C;) for '<'
    expect(html).toContain('&#x3C;script>')
  })

  it('handles multiple colors in one string', () => {
    const html = renderAnsi('\x1B[31mred\x1B[0m \x1B[32mgreen\x1B[0m')
    expect(html).toContain('red')
    expect(html).toContain('green')
    // Should have multiple styled spans
    const spanCount = (html.match(SPAN_TAG_RE) || []).length
    expect(spanCount).toBeGreaterThanOrEqual(2)
  })

  it('handles background colors', () => {
    const html = renderAnsi('\x1B[41mred bg\x1B[0m')
    expect(html).toContain('red bg')
    // Background colors produce --shiki-light-bg / --shiki-dark-bg variables
    expect(html).toContain('--shiki-light-bg:')
    expect(html).toContain('--shiki-dark-bg:')
  })

  it('strips non-SGR CSI controls before rendering', () => {
    const html = renderAnsi('before\x1B[2Kafter\x1B[A')
    expect(html.includes('\x1B')).toBe(false)
    expect(html).toContain('before')
    expect(html).toContain('after')
  })

  it('strips OSC controls before rendering while preserving visible text', () => {
    const html = renderAnsi('before\x1B]8;;https://example.com\x1B\\link\x1B]8;;\x1B\\after')
    expect(html.includes('\x1B')).toBe(false)
    expect(html).toContain('before')
    expect(html).toContain('link')
    expect(html).toContain('after')
  })

  describe('fallback on shiki error', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    it('falls back to escaped HTML when shiki throws', () => {
      vi.spyOn(shikiHighlighter, 'codeToHtml').mockImplementation(() => {
        throw new Error('regex engine failure')
      })

      const html = renderAnsi('<b>bold</b>')
      expect(html).toContain('<pre><code>')
      expect(html).toContain('&lt;b&gt;bold&lt;/b&gt;')
      expect(html).not.toContain('<b>')
    })

    it('strips terminal controls from fallback HTML', () => {
      vi.spyOn(shikiHighlighter, 'codeToHtml').mockImplementation(() => {
        throw new Error('regex engine failure')
      })

      const html = renderAnsi('before\x1B[2K<script>after</script>')
      expect(html.includes('\x1B')).toBe(false)
      expect(html).toContain('before&lt;script&gt;after&lt;/script&gt;')
    })
  })
})
