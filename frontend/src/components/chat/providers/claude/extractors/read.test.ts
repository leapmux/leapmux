import { describe, expect, it, vi } from 'vitest'
import { claudeReadFromToolResult } from './read'
import '../../testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async () => null,
}))

describe('claudeReadFromToolResult', () => {
  it('returns null for non-text variants', () => {
    for (const type of ['image', 'notebook', 'pdf', 'parts', 'file_unchanged']) {
      expect(claudeReadFromToolResult({
        toolUseResult: { type, file: { filePath: '/a' } },
        resultContent: '',
      })).toBeNull()
    }
  })

  it('extracts structured file payload', () => {
    const source = claudeReadFromToolResult({
      toolUseResult: {
        type: 'text',
        file: {
          filePath: '/tmp/a.ts',
          content: 'line1\nline2',
          startLine: 10,
          totalLines: 100,
          numLines: 2,
        },
      },
      resultContent: 'fallback',
    })
    expect(source).toEqual({
      filePath: '/tmp/a.ts',
      lines: [
        { num: 10, text: 'line1' },
        { num: 11, text: 'line2' },
      ],
      totalLines: 100,
      numLines: 2,
      fallbackContent: 'fallback',
    })
  })

  it('returns empty lines when structured content is empty', () => {
    const source = claudeReadFromToolResult({
      toolUseResult: { file: { filePath: '/a', content: '' } },
      resultContent: 'fallback',
    })
    expect(source).toEqual({
      filePath: '/a',
      lines: [],
      totalLines: 0,
      numLines: 0,
      fallbackContent: 'fallback',
    })
  })

  it('falls back to parsing raw cat-n content when no file payload', () => {
    const source = claudeReadFromToolResult({
      toolUseResult: undefined,
      resultContent: '1\tfoo\n2\tbar\n',
      toolInput: { file_path: '/sub.ts' },
    })
    expect(source).toEqual({
      filePath: '/sub.ts',
      lines: [
        { num: 1, text: 'foo' },
        { num: 2, text: 'bar' },
      ],
      totalLines: 0,
      numLines: 0,
      fallbackContent: '1\tfoo\n2\tbar\n',
    })
  })

  it('returns lines: null when raw content does not parse as cat-n', () => {
    const source = claudeReadFromToolResult({
      toolUseResult: undefined,
      resultContent: 'not a cat-n output',
    })
    expect(source).toEqual({
      filePath: '',
      lines: null,
      totalLines: 0,
      numLines: 0,
      fallbackContent: 'not a cat-n output',
    })
  })
})
