import { describe, expect, it } from 'vitest'
import { kiloSearchLines } from './search'

describe('kiloSearchLines', () => {
  it('preserves colons in paths and matching text', () => {
    expect(kiloSearchLines('Found 1 matches\n/project/a:2.ts:\n  Line 7: value: 3: more\n', 1)).toEqual([
      { filePath: '/project/a:2.ts', lineNumber: 7, text: 'value: 3: more' },
    ])
  })

  it('accepts Windows paths, CRLF, and empty matching lines', () => {
    expect(kiloSearchLines('Found 1 matches\r\nC:\\project\\file.ts:\r\n  Line 3: \r\n', 1)).toEqual([
      { filePath: 'C:\\project\\file.ts', lineNumber: 3, text: '' },
    ])
  })

  it.each([
    ['Found 1 matches\n/project/a:\n  Line 1: a', 2],
    ['Found 1 matches\n  Line 1: a', 1],
    ['Found 1 matches\n/project/a:\n  Line 0: a', 1],
    ['Found 1 matches\n/project/a:\n  Line 9007199254740993: a', 1],
    ['Found 1 matches\n/project/a:\n  Line 1: a\nunknown detail', 1],
    ['Found 1 matches\n/project/empty:\n/project/a:\n  Line 1: a', 1],
    ['Found 1 matches\n/project/a:\n  Line 1: a\n/project/empty:', 1],
    ['Found 1 matches\n/project/a:\n  Line 1: a', -1],
  ] as const)('keeps the raw fallback for unrecognized output %s', (text, count) => {
    expect(kiloSearchLines(text, count)).toBeNull()
  })

  it('does not remove a truncation notice that metadata did not confirm', () => {
    const text = 'Found 1 matches\n/project/a:\n  Line 1: a\n\n(Results truncated. Consider using a more specific path or pattern.)'
    expect(kiloSearchLines(text, 1)).toBeNull()
    expect(kiloSearchLines('Found 1 matches (more matches available)\n/project/a:\n  Line 1: a', 1)).toBeNull()
  })

  it('recognizes the native empty result', () => {
    expect(kiloSearchLines('No files found', 0)).toEqual([])
  })

  it('accepts a confirmed truncation footer only at the end', () => {
    const body = 'Found 1 matches (more matches available)\n/project/a:\n  Line 1: a\n\n(Results truncated. Consider using a more specific path or pattern.)\n'
    expect(kiloSearchLines(body, 1, true)).toHaveLength(1)
    expect(kiloSearchLines(`${body}more data`, 1, true)).toBeNull()
  })
})
