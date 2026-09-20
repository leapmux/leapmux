import { describe, expect, it } from 'vitest'
import { PI_SEARCH_TOOL } from '../protocol'
import { extractPiSearch } from './search'

/** A finished `tool_execution_end` payload for one search tool. */
function searchResult(toolName: string, text: string, details?: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'tool_execution_end',
    toolCallId: 'call-1',
    toolName,
    result: { content: [{ type: 'text', text }], ...(details ? { details } : {}) },
  }
}

/** Pi states a limit in its result details. The notice is only cut when one is confirmed. */
const LIMIT = { truncation: { truncated: true } }

describe('extractPiSearch truncation notice', () => {
  it('cuts the notice that Pi appends after a blank line', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'a.ts:1:hit\nb.ts:2:hit\n\n[Results limited to 2 matches]', LIMIT))
    expect(source?.content).toBe('a.ts:1:hit\nb.ts:2:hit')
    expect(source?.notice).toBe('Results limited to 2 matches')
    expect(source?.truncated).toBe(true)
  })

  // The notice is ONE bracketed line at the very end. A result whose last line holds a
  // path such as `app/[id]/page.tsx` also ends in `]` and holds a blank line above it,
  // and a pair of independent tests cut every line from that blank line onward.
  it('keeps a bracketed path that is not the notice', () => {
    const text = 'src/a.ts\n\nsrc/app/[id]/page.tsx'
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, text, LIMIT))
    expect(source?.filenames).toEqual(['src/a.ts', 'src/app/[id]/page.tsx'])
    expect(source?.notice).toBeUndefined()
  })

  it('keeps a bracketed last line that no blank line precedes', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, 'src/a.ts\nsrc/[id].tsx', LIMIT))
    expect(source?.filenames).toEqual(['src/a.ts', 'src/[id].tsx'])
    expect(source?.notice).toBeUndefined()
  })

  // Only the metadata decides that a limit was reached, so a result that merely looks
  // like it carries a notice keeps every line.
  it('keeps the notice text when no limit is confirmed', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, 'src/a.ts\n\n[Results limited]'))
    expect(source?.truncated).toBe(false)
    expect(source?.notice).toBeUndefined()
    expect(source?.filenames).toEqual(['src/a.ts', '[Results limited]'])
  })

  it('reports an empty notice for an empty pair of brackets', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, 'src/a.ts\n\n[]', LIMIT))
    expect(source?.filenames).toEqual(['src/a.ts'])
    expect(source?.notice).toBe('')
  })
})

/**
 * Whether Pi RECOGNIZED an empty result. Pi states its own wording for each half.
 *
 * The renderer decided this by comparing the raw text against LeapMux's own summary
 * prose, which put a provider's output format in the layer that knows no provider.
 * The extractor already read both sentences to build the counters, so it states the
 * fact instead.
 */
describe('extractPiSearch empty results', () => {
  it('reads the wording a find prints when it matched no file', () => {
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, 'No files found matching pattern'))?.empty).toBe(true)
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, '(empty directory)'))?.empty).toBe(true)
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, ''))?.empty).toBe(true)
  })

  it('reads the wording a grep prints when it matched nothing', () => {
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'No matches found'))?.empty).toBe(true)
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, ''))?.empty).toBe(true)
  })

  it('states no empty result for a search that found something', () => {
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Find, 'src/a.ts'))?.empty).toBe(false)
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'a.ts:1:hit'))?.empty).toBe(false)
  })
})

/**
 * The field arithmetic a grep result carries: one count for the match LINES, and one for
 * the distinct FILES they sit in.
 *
 * The two numbers differ the moment one file holds two matches, and the summary states
 * both. `grepMatches` is the shared reading of grep's own `path:line:text` contract; the
 * empty WORDING above it is Pi's own, and stays in this module.
 */
describe('extractPiSearch grep counters', () => {
  it('counts the files apart from the matches', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'a.ts:1:hit\na.ts:7:hit\nb.ts:2:hit'))
    expect(source?.numFiles).toBe(2)
    expect(source?.numLines).toBe(3)
    expect(source?.matchCount).toBe(3)
  })

  it('counts no line that states no match', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'a.ts:1:hit\n\nplain text with no line number'))
    expect(source?.numFiles).toBe(1)
    expect(source?.numLines).toBe(1)
  })

  it('leaves a colon inside the matched text out of the file count', () => {
    const source = extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'a.ts:1:see http://example.com:8080/x\na.ts:2:plain'))
    expect(source?.numFiles).toBe(1)
    expect(source?.numLines).toBe(2)
  })

  // Absent, never zero: a body this build read no match out of is a different statement
  // from a grep that matched nothing, and only Pi's own wording states the second one.
  it('states no match total for a body it read no match out of', () => {
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'unreadable output'))?.matchCount).toBeUndefined()
    expect(extractPiSearch(searchResult(PI_SEARCH_TOOL.Grep, 'No matches found'))?.matchCount).toBe(0)
  })
})
