import { describe, expect, it } from 'vitest'
import { parseCatNContent } from './ReadResultView'

describe('parseCatNContent', () => {
  it('parses tab-delimited lines', () => {
    const result = parseCatNContent('  1\tfoo\n  2\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('parses arrow-delimited lines', () => {
    const result = parseCatNContent('  1→foo\n  2→bar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('handles trailing empty line', () => {
    const result = parseCatNContent('  1\tfoo\n  2\tbar\n')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('returns null for empty input', () => {
    expect(parseCatNContent('')).toBeNull()
  })

  it('returns null for invalid input', () => {
    expect(parseCatNContent('not a cat-n line')).toBeNull()
  })

  it('returns null when any line is invalid', () => {
    expect(parseCatNContent('  1\tfoo\ninvalid\n  3\tbaz')).toBeNull()
  })

  it('parses lines with no leading whitespace', () => {
    const result = parseCatNContent('1\tfoo\n2\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('preserves content that contains tabs', () => {
    const result = parseCatNContent('  1\tfoo\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo\tbar' },
    ])
  })

  it('strips trailing [result-id: ...] metadata', () => {
    const result = parseCatNContent('1\tfoo\n2\tbar\n\n[result-id: r7]')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips [result-id: ...] with only trailing newline', () => {
    const result = parseCatNContent('1\tfoo\n[result-id: abc123]\n')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips trailing <system-reminder> block', () => {
    const result = parseCatNContent(
      '1\tfoo\n2\tbar\n\n<system-reminder>\nWhenever you read a file...\n</system-reminder>\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips multi-line <system-reminder> block with multiple body lines', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nline one\nline two\nline three\n</system-reminder>',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips <system-reminder> block followed by [result-id: ...]', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nreminder body\n</system-reminder>\n[result-id: r7]\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips multiple consecutive <system-reminder> blocks', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nfirst\n</system-reminder>\n<system-reminder>\nsecond\n</system-reminder>\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('returns null when </system-reminder> has no matching opening tag', () => {
    const result = parseCatNContent('1\tfoo\nstray text\n</system-reminder>\n')
    expect(result).toBeNull()
  })
})
