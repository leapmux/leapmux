import type { SearchResult } from '../model/searchResult'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { toolResultContentPre, toolResultPrompt } from '../toolStyles.css'
import { SearchResultBody } from './searchResult'

function source(overrides: Partial<SearchResult> = {}): SearchResult {
  return { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: false, ...overrides }
}

describe('search result display', () => {
  it('shortens a structured file path without changing punctuation in matching text', () => {
    const { container } = render(() => <SearchResultBody kind="search" source={source({ lines: [{ filePath: '/project/a:2.ts', lineNumber: 7, text: '/project/../literal:3:value' }] })} context={{ workingDir: '/project' }} />)
    expect(container.textContent).toBe('a:2.ts:7:/project/../literal:3:value')
  })
  it('preserves fallback text when a positive count has no structured matches', () => {
    const { container } = render(() => <SearchResultBody kind="search" source={source({ matchCount: 2, fallbackContent: 'The only match details' })} />)
    expect(container.textContent).toContain('2 matches')
    expect(container.textContent).toContain('The only match details')
  })

  // A grep that counted MATCHES and no files states the count on its own. Cursor
  // reports `totalMatches` with no file total, so this row read "No matches found"
  // for a search that found nine.
  it('states a grep match count that came with no file count', () => {
    const { container } = render(() => <SearchResultBody kind="grep" source={source({ matchCount: 9 })} />)
    expect(container.textContent).toContain('Found 9 matches')
  })

  // The three branches above it still answer first, so the new one changes no row
  // that already stated a number.
  it('keeps count mode and the line/file count ahead of it', () => {
    const counted = render(() => <SearchResultBody kind="grep" source={source({ mode: 'count', matchCount: 9, numFiles: 2 })} />)
    expect(counted.container.textContent).toContain('9 matches in 2 files')
    const lines = render(() => <SearchResultBody kind="grep" source={source({ matchCount: 9, numLines: 3, numFiles: 2 })} />)
    expect(lines.container.textContent).toContain('3 matches in 2 files')
  })

  it('states when the provider truncated its result', () => {
    const { container } = render(() => <SearchResultBody kind="search" source={source({ matchCount: 1, content: 'file.ts:2:match', truncated: true })} />)
    expect(container.textContent).toContain('file.ts:2:match')
    expect(container.textContent).toMatch(/truncated/i)
  })

  /**
   * The body never repeats the summary, whatever whitespace surrounds it.
   *
   * A length window guarded the comparison, and it allowed two characters of padding
   * where `trim()` removes any amount. "No matches found\n\n\n" is 19 characters
   * against a 16-character summary, so the window refused the very case it guards and
   * the row printed the same sentence twice.
   *
   * These cases draw the SEARCH kind, which words its summary from `matchCount`
   * alone. That is the branch where the guard still decides: for a grep or a glob the
   * body now draws nothing at all once `empty` is set, so a grep case would pass
   * without ever reaching the comparison.
   */
  it.each([
    ['no padding', 'No matches found'],
    ['trailing newlines', 'No matches found\n\n\n'],
    ['padding at both ends', '\n  No matches found  \n\n'],
  ])('draws no body that repeats the summary with %s', (_label, fallbackContent) => {
    const { container } = render(() => <SearchResultBody kind="search" source={source({ matchCount: 0, fallbackContent })} />)
    expect(container.textContent).toBe('No matches found')
  })

  it('keeps a body that says more than the summary', () => {
    const { container } = render(() => <SearchResultBody kind="search" source={source({ matchCount: 0, fallbackContent: 'No matches found in src/' })} />)
    expect(container.textContent).toContain('No matches found in src/')
  })

  /**
   * The EXTRACTOR states that a search found nothing. This layer never reads the
   * provider's bytes to decide it.
   *
   * `emptySummaryFor` compared `fallbackContent` against the very sentence it was
   * about to draw, which measured LeapMux's own user-interface prose against a
   * provider's output -- in the layer `results/README.md` keeps free of every
   * provider. A provider whose empty wording differed lost the muted marker and drew
   * its own sentence as body text instead.
   *
   * The counters cannot answer the question: a tool that found nothing and a body the
   * extractor could not classify both report no file, no line and no count.
   */
  describe('an empty result the extractor recognized', () => {
    it.each([
      ['grep', 'No matches found'],
      ['glob', 'No files found'],
    ] as const)('draws the %s marker once, whatever the provider wrote', (kind, marker) => {
      const { container } = render(() => (
        <SearchResultBody kind={kind} source={source({ empty: true, fallbackContent: 'nothing to see here' })} />
      ))
      expect(container.textContent).toBe(marker)
    })

    // The other half of the same rule. The bytes no longer decide, so a result the
    // extractor did NOT recognize keeps its own text and takes no marker -- even when
    // the text happens to read exactly like the marker.
    it.each([
      ['grep', 'No matches found'],
      ['glob', 'No files found'],
    ] as const)('draws no %s marker for a body it could not classify', (kind, marker) => {
      const { container } = render(() => (
        <SearchResultBody kind={kind} source={source({ empty: false, fallbackContent: marker })} />
      ))
      expect(container.getElementsByClassName(toolResultPrompt).length).toBe(0)
      expect(container.getElementsByClassName(toolResultContentPre)[0]?.textContent).toBe(marker)
    })

    // A recognized empty still states a truncation notice: the two facts are
    // independent, and the notice draws from its own branch.
    it('keeps a notice beside the marker', () => {
      const { container } = render(() => (
        <SearchResultBody kind="grep" source={source({ empty: true, fallbackContent: 'nothing', notice: 'limit: 10' })} />
      ))
      expect(container.textContent).toContain('No matches found')
      expect(container.textContent).toContain('limit: 10')
    })
  })

  /**
   * The file list follows a search that changes under it.
   *
   * `FileListView` takes entries and a search result holds bare paths, so the two meet
   * through a wrapper that this body caches BY PATH -- a fresh wrapper per read made
   * `<For>`, which keys by reference, throw the whole list away once per streamed
   * frame. The cache must not outlive what it describes: a path that leaves the list
   * and comes back has to draw once, in its own place.
   *
   * The saving itself has no seam a test can read here. `<For>` rebuilds its reactive
   * roots on a reference change, and the rows are bare text that the DOM
   * reconciliation then reuses either way, so the assertions below cover what the
   * cache can get WRONG rather than what it saves.
   */
  it('follows a file list that grows, shrinks and grows again', () => {
    const [filenames, setFilenames] = createSignal(['src/a.ts'])
    const { container } = render(() => (
      <SearchResultBody kind="glob" source={source({ filenames: filenames(), numFiles: filenames().length })} />
    ))
    const listText = () => {
      const list = container.getElementsByClassName(toolResultContentPre)[0]
      if (list === undefined)
        throw new Error('expected the file list to render')
      return list.textContent
    }
    expect(listText()).toBe('src/a.ts')

    setFilenames(['src/a.ts', 'src/b.ts'])
    expect(listText()).toBe('src/a.ts\nsrc/b.ts')

    setFilenames(['src/b.ts'])
    expect(listText()).toBe('src/b.ts')

    setFilenames(['src/a.ts', 'src/b.ts'])
    expect(listText()).toBe('src/a.ts\nsrc/b.ts')
  })
})
