import { describe, expect, it } from 'vitest'
import { formatChatQuote, formatFileMention, formatFileQuote } from './quoteUtils'

describe('formatFileQuote', () => {
  it('formats single-line selection with :lineNum', () => {
    expect(formatFileQuote('src/main.ts', 10, 10, 'const x = 1')).toBe(
      'At src/main.ts:10\n\n> ```\n> const x = 1\n> ```',
    )
  })

  it('formats multi-line selection with :startLine-endLine', () => {
    expect(formatFileQuote('src/main.ts', 10, 12, 'line1\nline2\nline3')).toBe(
      'At src/main.ts:10-12\n\n> ```\n> line1\n> line2\n> line3\n> ```',
    )
  })

  it('preserves empty lines in selected text', () => {
    expect(formatFileQuote('test.ts', 1, 3, 'a\n\nb')).toBe(
      'At test.ts:1-3\n\n> ```\n> a\n> \n> b\n> ```',
    )
  })
})

describe('formatChatQuote', () => {
  it('wraps single-line text as blockquote', () => {
    expect(formatChatQuote('hello')).toBe('> hello')
  })

  it('wraps multi-line text as blockquote', () => {
    expect(formatChatQuote('hello\nworld')).toBe('> hello\n> world')
  })

  it('handles empty lines', () => {
    expect(formatChatQuote('a\n\nb')).toBe('> a\n> \n> b')
  })
})

describe('formatFileMention', () => {
  it('prefixes path with @', () => {
    expect(formatFileMention('src/main.ts')).toBe('@src/main.ts')
  })

  it('works with simple filename', () => {
    expect(formatFileMention('package.json')).toBe('@package.json')
  })
})
