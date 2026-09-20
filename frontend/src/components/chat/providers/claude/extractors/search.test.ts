import type { ClaudeToolRow } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { claudeGlobSpec, claudeGrepSpec, claudeSearchFromToolResult, parseRawGrepGlobResult } from './search'
import { claudeRequestFor } from './toolRequests'

describe('parseRawGrepGlobResult', () => {
  it('parses "Found N files" summary and file list', () => {
    const raw = 'Found 2 files\n/a.ts\n/b.ts'
    expect(parseRawGrepGlobResult(raw, 'Glob')).toEqual({
      numFiles: 2,
      numLines: 0,
      filenames: ['/a.ts', '/b.ts'],
      content: '',
      empty: false,
    })
  })

  it('parses "N matches in M files" summary for Grep when data lines are file paths', () => {
    const raw = '5 matches in 3 files\n/a\n/b\n/c'
    // The parser preserves numFiles from the summary; numLines is reset to
    // 0 in the file-list branch (existing behavior).
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 3,
      numLines: 0,
      filenames: ['/a', '/b', '/c'],
      content: '',
      empty: false,
    })
  })

  it('returns content lines when Grep output looks like content mode', () => {
    const raw = '/a:1:foo\n/a:2:bar'
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 0,
      numLines: 2,
      filenames: [],
      content: '/a:1:foo\n/a:2:bar',
      empty: false,
    })
  })

  // The one wording the command line interface prints for a search that found
  // nothing. It is the parser that recognizes it, so the parser is what states the
  // fact -- a caller that spelled the sentence again would drift from the pattern.
  it('handles "No matches found" / "No files found"', () => {
    expect(parseRawGrepGlobResult('No matches found', 'Grep')).toEqual({
      numFiles: 0,
      numLines: 0,
      filenames: [],
      content: '',
      empty: true,
    })
    expect(parseRawGrepGlobResult('No files found', 'Glob').empty).toBe(true)
    expect(parseRawGrepGlobResult('', 'Glob').empty).toBe(true)
  })

  // A summary that HEADS a list is not an empty result, whatever it says. The body
  // decides, so a stray summary line cannot blank a list the tool did return.
  it('refuses to call a summary that heads a list empty', () => {
    expect(parseRawGrepGlobResult('No matches found\n/a.ts', 'Glob').empty).toBe(false)
    expect(parseRawGrepGlobResult('Found 2 files\n/a.ts\n/b.ts', 'Glob').empty).toBe(false)
  })

  it('parses count-mode trailing summary "Found N total occurrences across M files."', () => {
    // Claude's Grep `output_mode: "count"` raw text places per-file `path:count`
    // lines first and the summary on the last line (separated by a blank line).
    const raw = 'a/x.ts:5\na/y.ts:2\n\nFound 7 total occurrences across 2 files.'
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 2,
      numLines: 0,
      numMatches: 7,
      mode: 'count',
      filenames: [],
      content: 'a/x.ts:5\na/y.ts:2',
      empty: false,
    })
  })

  it('parses count-mode trailing summary in singular form', () => {
    const raw = 'a/x.ts:1\n\nFound 1 total occurrence across 1 file.'
    expect(parseRawGrepGlobResult(raw, 'Grep')).toEqual({
      numFiles: 1,
      numLines: 0,
      numMatches: 1,
      mode: 'count',
      filenames: [],
      content: 'a/x.ts:1',
      empty: false,
    })
  })

  it('parses count-mode trailing summary with pagination suffix', () => {
    const raw = 'a/x.ts:5\na/y.ts:2\n\nFound 7 total occurrences across 2 files. with pagination = limit: 2'
    const parsed = parseRawGrepGlobResult(raw, 'Grep')
    expect(parsed.mode).toBe('count')
    expect(parsed.numFiles).toBe(2)
    expect(parsed.numMatches).toBe(7)
    expect(parsed.filenames).toEqual([])
    expect(parsed.content).toBe('a/x.ts:5\na/y.ts:2')
  })
})

describe('claudeSearchFromToolResult grep', () => {
  it('builds source from structured result', () => {
    const source = claudeSearchFromToolResult('grep', {
      numFiles: 3,
      numLines: 5,
      filenames: ['/a', '/b', '/c'],
      content: 'matched line',
      mode: 'content',
      appliedLimit: 100,
    }, 'fallback')
    expect(source).toMatchObject({

      numFiles: 3,
      numLines: 5,
      filenames: ['/a', '/b', '/c'],
      content: 'matched line',
      mode: 'content',
      truncated: true,
      fallbackContent: 'fallback',
    })
  })

  it('marks truncated=false when appliedLimit is missing', () => {
    const source = claudeSearchFromToolResult('grep', { numFiles: 1, filenames: ['/a'] }, '')
    expect(source.truncated).toBe(false)
  })

  it('falls back to raw parsing when toolUseResult is null', () => {
    const source = claudeSearchFromToolResult('grep', null, 'Found 1 file\n/a.ts')
    expect(source.numFiles).toBe(1)
    expect(source.filenames).toEqual(['/a.ts'])
  })

  it('threads count-mode mode/numMatches through the subagent fallback', () => {
    const raw = 'a/x.ts:5\na/y.ts:2\n\nFound 7 total occurrences across 2 files.'
    const source = claudeSearchFromToolResult('grep', null, raw)
    expect(source.mode).toBe('count')
    expect(source.matchCount).toBe(7)
    expect(source.numFiles).toBe(2)
    expect(source.filenames).toEqual([])
    expect(source.content).toBe('a/x.ts:5\na/y.ts:2')
  })
})

describe('claudeSearchFromToolResult glob', () => {
  it('builds source from structured result with truncated and durationMs', () => {
    const source = claudeSearchFromToolResult('glob', {
      filenames: ['/a', '/b'],
      durationMs: 12,
      truncated: true,
    }, '')
    expect(source).toMatchObject({

      numFiles: 2,
      numLines: 0,
      truncated: true,
      durationMs: 12,
    })
  })

  it('falls back to raw parsing when toolUseResult is null', () => {
    const source = claudeSearchFromToolResult('glob', null, 'Found 2 files\n/a\n/b')
    expect(source.numFiles).toBe(2)
    expect(source.filenames).toEqual(['/a', '/b'])
  })
})

/**
 * Whether the tool RECOGNIZED an empty result, which only the extractor can answer.
 *
 * The renderer used to decide it by comparing the raw text against LeapMux's own
 * summary wording. The counters cannot answer it on their own: a grep that matched
 * nothing and a body this build could not read both report no file and no line.
 */
describe('claudeSearchFromToolResult empty results', () => {
  it('reads an empty grep from the counters the tool stated', () => {
    expect(claudeSearchFromToolResult('grep', { numFiles: 0, numLines: 0, filenames: [], content: '' }, 'No matches found').empty).toBe(true)
  })

  it('reads an empty glob from the file list the tool stated', () => {
    expect(claudeSearchFromToolResult('glob', { filenames: [] }, 'No files found').empty).toBe(true)
  })

  it('states no empty result for a structured answer that carries matches', () => {
    expect(claudeSearchFromToolResult('grep', { numFiles: 1, numLines: 2, content: 'a:1:hit' }, '').empty).toBe(false)
    expect(claudeSearchFromToolResult('glob', { filenames: ['/a.ts'] }, '').empty).toBe(false)
  })

  // The distinction the flag exists for. A structured object that stated NO counter
  // and no file list did not report "nothing found" -- it reported nothing this build
  // could read, and the raw text below it is still the only answer the row has.
  it('refuses to call an unreadable structured answer empty', () => {
    expect(claudeSearchFromToolResult('grep', { someOtherKey: 1 }, 'a body this build cannot classify').empty).toBe(false)
    expect(claudeSearchFromToolResult('glob', { someOtherKey: 1 }, 'a body this build cannot classify').empty).toBe(false)
  })

  it('carries the subagent parser answer through both variants', () => {
    expect(claudeSearchFromToolResult('grep', null, 'No matches found').empty).toBe(true)
    expect(claudeSearchFromToolResult('glob', null, 'No files found').empty).toBe(true)
    expect(claudeSearchFromToolResult('grep', null, '/a.ts:1:hit').empty).toBe(false)
  })
})

/** One Claude row, built from fields rather than from an envelope. */
function searchRow(overrides: Partial<ClaudeToolRow> = {}): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'result',
    toolName: 'Grep',
    input: {},
    toolUseResult: undefined,
    resultContent: '',
    rawResultContent: undefined,
    images: [],
    isError: undefined,
    ...overrides,
  }
}

/** The declared request both search kinds answer for the same arguments. */
const SEARCH_REQUEST = claudeRequestFor('grep', { pattern: 'needle' }, { toolName: 'Grep', result: undefined, context: {} })

/**
 * A failed search states its reason, not a match.
 *
 * The reason is the only thing a failed call carries -- no `tool_use_result` rides
 * beside it -- so it reached the subagent parser, which classifies a line it cannot
 * read as content as a FILE NAME. The row then drew "Found 1 file" over a file list
 * holding the sentence, and `relativizePath` shortened the sentence like a path.
 */
describe('claudeGrepSpec', () => {
  it('states the reason alone for a search the tool failed', () => {
    const result = searchRow({ isError: true, resultContent: 'File does not exist.' })
    expect(claudeGrepSpec(SEARCH_REQUEST, result).result).toStrictEqual({ failure: true, text: 'File does not exist.' })
  })

  it('reads the search result for a call that did not fail', () => {
    const result = searchRow({ resultContent: 'Found 1 file\n/a.ts' })
    expect(claudeGrepSpec(SEARCH_REQUEST, result).result).toMatchObject({ numFiles: 1, filenames: ['/a.ts'] })
  })

  it('states no result for a call that has not answered', () => {
    expect(claudeGrepSpec(SEARCH_REQUEST, undefined).result).toBeUndefined()
  })
})

describe('claudeGlobSpec', () => {
  it('states the reason alone for a search the tool failed', () => {
    const result = searchRow({ toolName: 'Glob', isError: true, resultContent: 'EISDIR: illegal operation' })
    expect(claudeGlobSpec(SEARCH_REQUEST, result).result).toStrictEqual({ failure: true, text: 'EISDIR: illegal operation' })
  })

  it('reads the file list for a call that did not fail', () => {
    const result = searchRow({ toolName: 'Glob', resultContent: 'Found 2 files\n/a.ts\n/b.ts' })
    expect(claudeGlobSpec(SEARCH_REQUEST, result).result).toMatchObject({ numFiles: 2, filenames: ['/a.ts', '/b.ts'] })
  })
})

/**
 * `filenames` arrives off the wire, and every entry reaches `relativizePath`, which
 * calls string methods on it. One non-string element threw there and took the whole
 * transcript row with it, so the reading keeps the STRING entries and drops the rest.
 */
describe('claudeSearchFromToolResult filenames', () => {
  it('drops a non-string entry from a grep file list', () => {
    const source = claudeSearchFromToolResult('grep', { numFiles: 2, filenames: ['/a.ts', 42, null, { path: '/b.ts' }] }, '')
    expect(source.filenames).toStrictEqual(['/a.ts'])
    expect(source.filenames.every(name => typeof name === 'string')).toBe(true)
  })

  it('drops a non-string entry from a glob file list', () => {
    const source = claudeSearchFromToolResult('glob', { filenames: [42, '/b.ts'] }, '')
    expect(source.filenames).toStrictEqual(['/b.ts'])
    expect(source.numFiles).toBe(1)
  })

  // The tool STATED a list here, so it did not report "no file matched". The raw length
  // decides that, because a filtered list that emptied says only that this build could
  // not read the entries -- and the raw text below is still the row's whole answer.
  it('refuses to call a glob list of unreadable entries empty', () => {
    expect(claudeSearchFromToolResult('glob', { filenames: [42] }, 'raw body').empty).toBe(false)
    expect(claudeSearchFromToolResult('glob', { filenames: [] }, 'No files found').empty).toBe(true)
  })

  it('answers an empty list for a `filenames` that is no array at all', () => {
    expect(claudeSearchFromToolResult('glob', { filenames: 'a.ts' }, '').filenames).toStrictEqual([])
    expect(claudeSearchFromToolResult('glob', { filenames: 'a.ts' }, '').empty).toBe(false)
  })
})
