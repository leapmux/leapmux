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

          content: 'line1\nline2',
          startLine: 10,
        },
      },
      resultContent: 'fallback',
    })
    expect(source).toEqual({

      lines: [
        { num: 10, text: 'line1' },
        { num: 11, text: 'line2' },
      ],
      fallbackContent: 'fallback',
      leading: [],
      trailing: [],
    })
  })

  it('returns empty lines when structured content is empty', () => {
    const source = claudeReadFromToolResult({
      toolUseResult: { file: { filePath: '/a', content: '' } },
      resultContent: 'fallback',
    })
    expect(source).toEqual({

      lines: [],
      fallbackContent: 'fallback',
      leading: [],
      trailing: [],
    })
  })

  it('falls back to parsing raw cat-n content when no file payload', () => {
    const source = claudeReadFromToolResult({
      resultContent: '1\tfoo\n2\tbar\n',
    })
    expect(source).toEqual({

      lines: [
        { num: 1, text: 'foo' },
        { num: 2, text: 'bar' },
      ],
      fallbackContent: '1\tfoo\n2\tbar\n',
      leading: [],
      trailing: [],
    })
  })

  it('returns lines: null when raw content does not parse as cat-n', () => {
    const source = claudeReadFromToolResult({
      resultContent: 'not a cat-n output',
    })
    expect(source).toEqual({

      lines: null,
      fallbackContent: 'not a cat-n output',
      leading: [],
      trailing: [],
    })
  })
})
