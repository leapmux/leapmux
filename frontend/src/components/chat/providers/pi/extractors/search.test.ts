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
