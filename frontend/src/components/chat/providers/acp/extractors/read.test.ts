import { describe, expect, it } from 'vitest'
import { acpReadFromToolCall } from './read'

describe('acpReadFromToolCall', () => {
  it('returns null for missing toolUse', () => {
    expect(acpReadFromToolCall(null)).toBeNull()
    expect(acpReadFromToolCall(undefined)).toBeNull()
  })

  it('returns null when neither filePath nor parseable cat-n is present', () => {
    expect(acpReadFromToolCall({
      content: [{ type: 'content', content: { text: 'plain text' } }],
    })).toBeNull()
  })

  it('parses cat-n output and exposes lines', () => {
    const source = acpReadFromToolCall({
      rawInput: { filePath: '/tmp/a.ts' },
      content: [
        { type: 'content', content: { text: '1\tfoo\n2\tbar\n' } },
      ],
    })
    expect(source).toEqual({
      filePath: '/tmp/a.ts',
      lines: [
        { num: 1, text: 'foo' },
        { num: 2, text: 'bar' },
      ],
      totalLines: 0,
      numLines: 0,
      fallbackContent: '1\tfoo\n2\tbar\n',
    })
  })

  it('returns lines: null when output is not cat-n format but filePath exists', () => {
    const source = acpReadFromToolCall({
      rawInput: { filePath: '/tmp/a.ts' },
      content: [
        { type: 'content', content: { text: 'plain text body' } },
      ],
    })
    expect(source).toEqual({
      filePath: '/tmp/a.ts',
      lines: null,
      totalLines: 0,
      numLines: 0,
      fallbackContent: 'plain text body',
    })
  })

  it('falls back to rawOutput.output when no content array text is present', () => {
    const source = acpReadFromToolCall({
      rawInput: { filePath: '/x' },
      rawOutput: { output: '1\tabc\n' },
    })
    expect(source?.lines).toEqual([{ num: 1, text: 'abc' }])
  })

  it('uses parseable cat-n alone (no filePath) as a valid source', () => {
    const source = acpReadFromToolCall({
      content: [{ type: 'content', content: { text: '1\thi\n' } }],
    })
    expect(source).toEqual({
      filePath: '',
      lines: [{ num: 1, text: 'hi' }],
      totalLines: 0,
      numLines: 0,
      fallbackContent: '1\thi\n',
    })
  })
})
