import type { SearchResult } from './searchResult'
import { describe, expect, it } from 'vitest'
import { COLLAPSED_RESULT_ROWS } from './collapse'
import { searchResultCollapsible, searchResultCopyable, searchResultText } from './searchResult'

function search(overrides: Partial<SearchResult> = {}): SearchResult {
  return { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false, ...overrides }
}

const longText = Array.from({ length: COLLAPSED_RESULT_ROWS + 4 }, (_, row) => `line ${row}`).join('\n')

describe('searchResultText', () => {
  it('draws the structured matches when the tool recovered any', () => {
    const source = search({ lines: [{ filePath: '/repo/a.ts', lineNumber: 7, text: 'hit' }], content: 'ignored' })
    expect(searchResultText(source)).toBe('/repo/a.ts:7:hit')
  })

  it('falls through to the blob when the structured list came back empty', () => {
    expect(searchResultText(search({ lines: [], content: 'a:1:hit' }))).toBe('a:1:hit')
  })

  it('falls through to the raw output when there is no blob either', () => {
    expect(searchResultText(search({ lines: [], fallbackContent: longText }))).toBe(longText)
  })
})

/**
 * An EMPTY `lines` array is the case both readers must answer the same way.
 *
 * `cursorSearchSource` sets `lines` on every result, empty included, so a grep whose
 * structured parse recovered nothing still carries the field. One reader took that for
 * "no rows, nothing to expand" while the body drew `fallbackContent` through the
 * collapser -- the reader saw a clipped output and no Expand control, with no route to
 * the rest of it.
 */
describe('searchResultCollapsible', () => {
  it('measures the TEXT when the structured list came back empty', () => {
    const source = search({ lines: [], fallbackContent: longText })
    expect(searchResultText(source)).toBe(longText)
    expect(searchResultCollapsible(source)).toBe(true)
  })

  it('agrees with searchResultText for an empty list and a short output', () => {
    expect(searchResultCollapsible(search({ lines: [], fallbackContent: 'one line' }))).toBe(false)
  })

  it('counts the structured rows when the tool recovered any', () => {
    const lines = Array.from({ length: COLLAPSED_RESULT_ROWS + 1 }, (_, row) => ({ filePath: `/f${row}.ts`, text: 'hit' }))
    expect(searchResultCollapsible(search({ lines }))).toBe(true)
    expect(searchResultCollapsible(search({ lines: lines.slice(0, COLLAPSED_RESULT_ROWS) }))).toBe(false)
  })

  it('collapses a long file list whatever the matches say', () => {
    const filenames = Array.from({ length: COLLAPSED_RESULT_ROWS + 1 }, (_, row) => `/f${row}.ts`)
    expect(searchResultCollapsible(search({ filenames, lines: [] }))).toBe(true)
  })

  it('measures the text when no structured list was ever set', () => {
    expect(searchResultCollapsible(search({ content: longText }))).toBe(true)
  })
})

/**
 * The FILE LIST is the answer for a search that returned one, and the body draws it.
 * Without the fallback the toolbar read `hasCopyable` as false and hid the Copy button
 * over a body that visibly listed files -- only the glob renderer patched that, so a
 * grep in `files_with_matches` mode and an ACP search offered nothing to copy.
 */
describe('searchResultCopyable', () => {
  it('answers the file list when there is no match text', () => {
    const source = search({ filenames: ['/repo/a.ts', '/repo/b.ts'], numFiles: 2 })
    expect(searchResultText(source)).toBe('')
    expect(searchResultCopyable(source)).toBe('/repo/a.ts\n/repo/b.ts')
  })

  it('prefers the match text when the search returned one', () => {
    const source = search({ filenames: ['/repo/a.ts'], lines: [{ filePath: '/repo/a.ts', lineNumber: 3, text: 'hit' }] })
    expect(searchResultCopyable(source)).toBe('/repo/a.ts:3:hit')
  })

  it('answers nothing for a search that found nothing', () => {
    expect(searchResultCopyable(search())).toBe('')
  })
})
