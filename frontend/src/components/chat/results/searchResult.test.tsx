import type { SearchResultSource } from './searchResult'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SearchResultBody } from './searchResult'

function source(overrides: Partial<SearchResultSource> = {}): SearchResultSource {
  return { variant: 'search', filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', ...overrides }
}

describe('search result display', () => {
  it('shortens a structured file path without changing punctuation in matching text', () => {
    const { container } = render(() => <SearchResultBody source={source({ lines: [{ filePath: '/project/a:2.ts', lineNumber: 7, text: '/project/../literal:3:value' }] })} context={{ workingDir: '/project' }} />)
    expect(container.textContent).toBe('a:2.ts:7:/project/../literal:3:value')
  })
  it('preserves fallback text when a positive count has no structured matches', () => {
    const { container } = render(() => <SearchResultBody source={source({ matches: 2, fallbackContent: 'The only match details' })} />)
    expect(container.textContent).toContain('2 matches')
    expect(container.textContent).toContain('The only match details')
  })

  it('states when the provider truncated its result', () => {
    const { container } = render(() => <SearchResultBody source={source({ matches: 1, content: 'file.ts:2:match', truncated: true })} />)
    expect(container.textContent).toContain('file.ts:2:match')
    expect(container.textContent).toMatch(/truncated/i)
  })
})
