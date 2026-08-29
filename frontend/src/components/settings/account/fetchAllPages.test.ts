import { describe, expect, it } from 'vitest'
import { fetchAllPages, MAX_PAGES, PAGE_SIZE } from './fetchAllPages'

function page(items: string[], nextCursor: string) {
  return Promise.resolve({ items, nextCursor })
}

describe('fetchAllPages reads every page of a keyset listing', () => {
  it('follows the cursor until the server ends the listing', async () => {
    const cursors: string[] = []
    const rows = await fetchAllPages(
      async (cursor) => {
        cursors.push(cursor)
        return cursor === ''
          ? page(['a', 'b'], 'c1')
          : page(['c'], '')
      },
      { maxPages: 10, keyOf: x => x },
    )
    expect(cursors).toEqual(['', 'c1'])
    expect(rows).toEqual(['a', 'b', 'c'])
  })

  it('stops when the cursor fails to advance instead of looping', async () => {
    let calls = 0
    const rows = await fetchAllPages(
      async () => {
        calls++
        return page(['a'], 'stuck')
      },
      { maxPages: 10, keyOf: x => x },
    )
    expect(calls).toBe(2)
    expect(rows).toEqual(['a'])
  })

  it('stops at maxPages even if the server keeps answering', async () => {
    let calls = 0
    const rows = await fetchAllPages(
      async () => {
        calls++
        return page([`row-${calls}`], `c${calls}`)
      },
      { maxPages: 3, keyOf: x => x },
    )
    expect(calls).toBe(3)
    expect(rows).toEqual(['row-1', 'row-2', 'row-3'])
  })

  it('drops a row the server repeats across a keyset boundary', async () => {
    const rows = await fetchAllPages(
      async cursor => (cursor === '' ? page(['a', 'b'], 'c1') : page(['b', 'c'], '')),
      { maxPages: 5, keyOf: x => x },
    )
    expect(rows).toEqual(['a', 'b', 'c'])
  })

  it('carries the runaway-guard constants the panels share', () => {
    expect(PAGE_SIZE).toBe(500)
    expect(MAX_PAGES).toBe(500)
  })
})
