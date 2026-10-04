import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleCorpusSearchResult, codewhaleFileSearchResult, codewhaleGrepResult } from './search'

describe('codewhaleGrepResult', () => {
  it('reads each match with its file and line, and the tool\'s own counters', () => {
    const text = JSON.stringify({ matches: [{ file: 'a.ts', line_number: 2, line: 'a:b' }, { file: 'a.ts', line_number: 5, line: 'c' }, { line: 'no file' }], total_matches: 9, truncated: true })
    expect(codewhaleGrepResult(text)).toStrictEqual({
      filenames: ['a.ts'],
      content: '',
      lines: [{ filePath: 'a.ts', lineNumber: 2, text: 'a:b' }, { filePath: 'a.ts', lineNumber: 5, text: 'c' }],
      numFiles: 1,
      numLines: 2,
      matchCount: 9,
      truncated: true,
      fallbackContent: text,
      empty: false,
    })
  })

  it('states an empty search as empty', () => {
    expect(codewhaleGrepResult('{"matches":[]}')?.empty).toBe(true)
  })

  // A counter the tool leaves out states nothing, so the body counts the matches
  // it holds rather than a zero that the tool never reported.
  it('states no line number, no total and no cut that the tool did not report', () => {
    const text = JSON.stringify({ matches: [{ file: 'a.ts', line: 'x' }] })
    expect(codewhaleGrepResult(text)).toStrictEqual({
      filenames: ['a.ts'],
      content: '',
      lines: [{ filePath: 'a.ts', text: 'x' }],
      numFiles: 1,
      numLines: 1,
      truncated: false,
      fallbackContent: text,
      empty: false,
    })
  })

  it('counts each file once, in the order it first matched', () => {
    const text = JSON.stringify({ matches: [{ file: 'b.ts', line_number: 1, line: 'x' }, { file: 'a.ts', line_number: 1, line: 'x' }, { file: 'b.ts', line_number: 9, line: 'x' }] })
    expect(codewhaleGrepResult(text)).toMatchObject({ filenames: ['b.ts', 'a.ts'], numFiles: 2, numLines: 3 })
  })

  it('answers null for text that is not the document', () => {
    expect(codewhaleGrepResult('Failed to search')).toBeNull()
    expect(codewhaleGrepResult('[]')).toBeNull()
  })
})

describe('codewhaleFileSearchResult', () => {
  it('reads the paths, best match first', () => {
    const text = JSON.stringify([{ path: 'src/a.ts', name: 'a.ts', score: 2 }, { name: 'nameless' }])
    expect(codewhaleFileSearchResult(text)).toMatchObject({ filenames: ['src/a.ts'], numFiles: 1, empty: false })
    expect(codewhaleFileSearchResult('[]')?.empty).toBe(true)
    expect(codewhaleFileSearchResult('{}')).toBeNull()
    expect(codewhaleFileSearchResult('nope')).toBeNull()
  })
})

describe('codewhaleCorpusSearchResult', () => {
  it('lists the tools a registry search found', () => {
    const text = JSON.stringify({ tool_references: [{ tool_name: 'request_user_input' }, { tool_name: 'tasks' }, {}] })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.ToolSearch, text)).toMatchObject({ filenames: [], content: 'request_user_input\ntasks', numLines: 2, matchCount: 2, empty: false })
  })

  // A tool that the registry lists as unavailable is one the model cannot call, so
  // it is not a match.
  it('states a registry search that found no callable tool as empty', () => {
    const text = JSON.stringify({ tool_references: [], unavailable_tool_references: [{ tool_name: 'github' }] })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.ToolSearch, text)).toStrictEqual({
      filenames: [],
      content: '',
      numFiles: 0,
      numLines: 0,
      matchCount: 0,
      truncated: false,
      fallbackContent: text,
      empty: true,
    })
  })

  // The registry document is the answer of `tool_search` alone. Another search
  // that prints the same JSON prints text.
  it('reads the registry document only for the registry search', () => {
    const text = JSON.stringify({ tool_references: [{ tool_name: 'tasks' }] })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.Lsp, text)).toMatchObject({ content: text, numLines: 1 })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.Lsp, text)).not.toHaveProperty('matchCount')
  })

  it('states the text of any other search as its matches', () => {
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.Lsp, 'src/b.ts:10\n\n')).toMatchObject({ content: 'src/b.ts:10\n\n', numLines: 1, empty: false })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.ToolSearch, 'not json')).toMatchObject({ content: 'not json', numLines: 1 })
    expect(codewhaleCorpusSearchResult(CODEWHALE_TOOL.Lsp, '')).toMatchObject({ numLines: 0, empty: true })
  })
})
