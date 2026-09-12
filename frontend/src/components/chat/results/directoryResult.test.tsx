import type { DirectoryResultSource } from './directoryResult'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { DirectoryResultBody, directoryResultCollapsible } from './directoryResult'

describe('directory result display', () => {
  it.each([
    [{ entries: [] }, 'Empty directory'],
    [{ entries: [], totalEntries: 4, offset: 8 }, 'No entries shown'],
    [{ entries: [], truncated: true }, 'No entries shown'],
    [{ entries: [{ path: 'a' }], totalEntries: 1 }, '1 entry'],
    [{ entries: [{ path: 'a' }, { path: 'src/' }], totalEntries: 8, offset: 3 }, 'Entries 3–4 of 8'],
  ] as Array<[DirectoryResultSource, string]>)('describes the selected range %j', (source, expected) => {
    const { container } = render(() => <DirectoryResultBody source={source} />)
    expect(container.textContent).toContain(expected)
  })

  it('shows only the collapsed entries and keeps the provider limit notice', () => {
    const source = { entries: ['first', 'second', 'third', 'fourth'].map(path => ({ path })), notice: 'Provider entry limit reached' }
    const { container } = render(() => <DirectoryResultBody source={source} />)
    expect(directoryResultCollapsible(source)).toBe(true)
    expect(container.textContent).toContain('first')
    expect(container.textContent).not.toContain('fourth')
    expect(container.textContent).toContain(source.notice)
  })
})
