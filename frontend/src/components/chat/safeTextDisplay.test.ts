import { describe, expect, it } from 'vitest'
import {
  largeMarkdownPlainHtml,
  LIMITED_TEXT_DISPLAY_NOTICE,
  limitTextForDisplay,
  MARKDOWN_PARSE_CHAR_LIMIT,
  markdownNeedsPlainTextDisplay,
} from './safeTextDisplay'

describe('safe text display', () => {
  it('keeps a small value unchanged', () => {
    expect(limitTextForDisplay('first\nsecond')).toEqual({ text: 'first\nsecond', limited: false })
  })

  it('keeps the start and end when it limits total characters', () => {
    const result = limitTextForDisplay(`start-${'x'.repeat(200)}-end`, { maxChars: 80, maxLineChars: 80 })

    expect(result.limited).toBe(true)
    expect(result.text).toContain('start-')
    expect(result.text).toContain('-end')
    expect(result.text).toContain('content omitted from display')
    expect(result.text.length).toBeLessThanOrEqual(80)
  })

  it('limits one line without removing its end', () => {
    const result = limitTextForDisplay(`start-${'x'.repeat(200)}-end`, { maxChars: 1_000, maxLineChars: 40 })

    expect(result.limited).toBe(true)
    expect(result.text).toContain('start-')
    expect(result.text).toContain('-end')
    expect(result.text.length).toBeLessThanOrEqual(40)
  })

  it('keeps the first and last lines when it limits the line count', () => {
    const text = Array.from({ length: 2_000 }, (_, index) => `line-${index}`).join('\n')
    const result = limitTextForDisplay(text, { maxChars: 100_000, maxLineChars: 100, maxLines: 100 })

    expect(result.limited).toBe(true)
    expect(result.text).toContain('line-0')
    expect(result.text).toContain('line-1999')
    expect(result.text).toContain('lines omitted from display')
    expect(result.text.split('\n').length).toBeLessThanOrEqual(101)
  })

  it('does not split a surrogate pair at either retained edge', () => {
    const result = limitTextForDisplay(`start${'x'.repeat(30)}😀${'y'.repeat(30)}end`, { maxChars: 40, maxLineChars: 40 })

    expect(result.text).not.toContain('\uFFFD')
    expect(() => new TextEncoder().encode(result.text)).not.toThrow()
  })

  it('routes large or long-line Markdown to the plain display', () => {
    expect(markdownNeedsPlainTextDisplay('x'.repeat(MARKDOWN_PARSE_CHAR_LIMIT + 1))).toBe(true)
    expect(markdownNeedsPlainTextDisplay('x'.repeat(5_000))).toBe(true)
    expect(markdownNeedsPlainTextDisplay('# Small\n\nBody')).toBe(false)
  })

  it('escapes large Markdown before it builds the plain display', () => {
    const html = largeMarkdownPlainHtml(`<script>unsafe</script>${'x'.repeat(100_000)}`)

    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;unsafe&lt;/script&gt;')
    expect(html).toContain(LIMITED_TEXT_DISPLAY_NOTICE)
  })
})
