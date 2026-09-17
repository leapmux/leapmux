import type { ListResult } from '../ir/tools/list'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { listResultCollapsible } from '../ir/tools/list'
import { ListResultBody } from './listResult'

describe('the list result body (ListResultBody)', () => {
  it.each([
    [{ entries: [] }, 'Empty directory'],
    [{ entries: [], totalEntries: 4, offset: 8 }, 'No entries shown'],
    [{ entries: [], truncated: true }, 'No entries shown'],
    [{ entries: [{ path: 'a' }], totalEntries: 1 }, '1 entry'],
    [{ entries: [{ path: 'a' }, { path: 'src/' }], totalEntries: 8, offset: 3 }, 'Entries 3–4 of 8'],
  ] as Array<[ListResult, string]>)('describes the selected range %j', (source, expected) => {
    const { container } = render(() => <ListResultBody source={source} />)
    expect(container.textContent).toContain(expected)
  })

  it('shows only the collapsed entries and keeps the provider limit notice', () => {
    const source = { entries: ['first', 'second', 'third', 'fourth'].map(path => ({ path })), notice: 'Provider entry limit reached' }
    const { container } = render(() => <ListResultBody source={source} />)
    expect(listResultCollapsible(source)).toBe(true)
    expect(container.textContent).toContain('first')
    expect(container.textContent).not.toContain('fourth')
    expect(container.textContent).toContain(source.notice)
  })
})
