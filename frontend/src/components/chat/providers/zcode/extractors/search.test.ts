import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { extractZCodeSearch } from './search'
import { zcodeRow } from './toolCommon'

function source(name: string, content: string, args: Record<string, unknown> = {}, success = true) {
  const request = input({ type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'call', toolName: name, input: args } })
  return extractZCodeSearch(zcodeRow({ type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { content, success } } }, name, request))
}

describe('zcode search output', () => {
  it('preserves trailing spaces in filenames', () => {
    expect(source('Glob', 'first.ts\nspace.ts ')?.filenames).toEqual(['first.ts', 'space.ts '])
    expect(source('Grep', 'Found 2 files\nfirst.ts\nspace.ts ')?.filenames).toEqual(['first.ts', 'space.ts '])
  })

  it.each(['', 'No files found'])('handles empty glob results: %s', (text) => {
    expect(source('Glob', text)).toMatchObject({ filenames: [], numFiles: 0, truncated: false })
  })

  it('keeps pagination separate from the file list', () => {
    expect(source('Grep', 'Found 2 files limit: 2, offset: 4\nfirst.ts\nsecond.ts')).toMatchObject({ filenames: ['first.ts', 'second.ts'], numFiles: 2, notice: 'limit: 2, offset: 4', truncated: false })
  })

  it('preserves explicit zero matches and files in count mode', () => {
    expect(source('Grep', 'No matches found\n\nFound 0 total occurrences across 0 files.', { output_mode: 'count' })).toMatchObject({ numMatches: 0, numFiles: 0, content: 'No matches found' })
  })

  it('keeps context lines without inventing match counts', () => {
    expect(source('Grep', 'a.ts:1:context\na.ts:2:match\n\n[Showing results with pagination = limit: 250, offset: 0]', { output_mode: 'content', context: 1 })).toMatchObject({ content: 'a.ts:1:context\na.ts:2:match', numLines: 0, notice: 'limit: 250, offset: 0', truncated: false })
  })

  it('retains malformed counts as plain output', () => {
    const text = 'a.ts:1\n\nFound 9007199254740992 total occurrences across 1 file.'
    expect(source('Grep', text, { output_mode: 'count' })).toMatchObject({ numFiles: 0, fallbackContent: text })
    expect(source('Grep', text, { output_mode: 'count' })?.numMatches).toBeUndefined()
  })

  it('does not parse failed searches or unrelated tools as file lists', () => {
    expect(source('Glob', 'Permission denied', {}, false)).toBeNull()
    expect(source('Other', 'first.ts')).toBeNull()
  })
})
