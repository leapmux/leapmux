import type { PageRequestInit } from './fetchAllPages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { MAX_PAGE_LIMIT } from '~/generated/contracts/pagination'
import { PageResponseSchema } from '~/generated/proto/leapmux/v1/common_pb'
import { fetchAllPages, MAX_PAGES } from './fetchAllPages'

function page(items: string[], nextCursor: string) {
  return Promise.resolve({
    items,
    page: create(PageResponseSchema, { nextCursor }),
  })
}

describe('fetchAllPages', () => {
  it('follows the cursor until the server ends the listing', async () => {
    const requests: PageRequestInit[] = []
    const rows = await fetchAllPages(
      async (req) => {
        requests.push(req)
        return req.cursor === ''
          ? page(['a', 'b'], 'c1')
          : page(['c'], '')
      },
      {
        maxPages: 10,
        keyOf: x => x,
      },
    )
    // Every request asks for the hub's maximum page, so an omitted limit
    // never shrinks the listing to the hub's default.
    expect(requests).toEqual([
      { limit: MAX_PAGE_LIMIT, cursor: '' },
      { limit: MAX_PAGE_LIMIT, cursor: 'c1' },
    ])
    expect(rows).toEqual(['a', 'b', 'c'])
  })

  it('ends the listing when a response carries no page', async () => {
    let calls = 0
    const rows = await fetchAllPages(
      async () => {
        calls++
        return {
          items: ['a'],
        }
      },
      {
        keyOf: x => x,
      },
    )
    expect(calls).toBe(1)
    expect(rows).toEqual(['a'])
  })

  it('returns no rows for an empty listing', async () => {
    const rows = await fetchAllPages(async () => page([], ''), {
      keyOf: x => x,
    })
    expect(rows).toEqual([])
  })

  // The hub ends every listing with an empty cursor, so a guard that stops the
  // loop means a fault in the hub. The rows read so far are not the listing,
  // and a caller that drew them would present part of it as all of it.
  it('rejects when the cursor fails to advance instead of looping', async () => {
    let calls = 0
    await expect(fetchAllPages(
      async () => {
        calls++
        return page(['a'], 'stuck')
      },
      {
        maxPages: 10,
        keyOf: x => x,
      },
    )).rejects.toThrow('The hub did not end the listing: the next-page cursor did not advance.')
    expect(calls).toBe(2)
  })

  it('rejects after maxPages pages when the server keeps answering', async () => {
    let calls = 0
    await expect(fetchAllPages(
      async () => {
        calls++
        return page([`row-${calls}`], `c${calls}`)
      },
      {
        maxPages: 3,
        keyOf: x => x,
      },
    )).rejects.toThrow('The hub did not end the listing: it continued past 3 pages.')
    expect(calls).toBe(3)
  })

  // The boundary of the guard above: the last allowed page may end the
  // listing, and that listing is complete.
  it('resolves a listing that ends on the last allowed page', async () => {
    let calls = 0
    const rows = await fetchAllPages(
      async () => {
        calls++
        return page([`row-${calls}`], calls === 3
          ? ''
          : `c${calls}`)
      },
      {
        maxPages: 3,
        keyOf: x => x,
      },
    )
    expect(calls).toBe(3)
    expect(rows).toEqual(['row-1', 'row-2', 'row-3'])
  })

  // A limit that admits no page cannot read the listing, so it cannot answer
  // one either. An empty result would read as an empty listing.
  it.each([0, -1, Number.NaN, 1.5])('rejects maxPages %s without reading a page', async (maxPages) => {
    const fetchPage = vi.fn(async () => page(['a'], ''))
    await expect(fetchAllPages(fetchPage, {
      maxPages,
      keyOf: x => x,
    })).rejects.toThrow(RangeError)
    expect(fetchPage).not.toHaveBeenCalled()
  })

  it('rejects after MAX_PAGES pages when the caller gives no maxPages', async () => {
    let calls = 0
    await expect(fetchAllPages(
      async () => {
        calls++
        return page([`row-${calls}`], `c${calls}`)
      },
      {
        keyOf: x => x,
      },
    )).rejects.toThrow(`The hub did not end the listing: it continued past ${MAX_PAGES} pages.`)
    expect(calls).toBe(MAX_PAGES)
  })

  it('keeps the last copy of a row the server repeats across a keyset boundary', async () => {
    const rows = await fetchAllPages(
      async req => (req.cursor === ''
        ? Promise.resolve({
            items: [
              { id: 'a', v: 1 },
              { id: 'b', v: 1 },
            ],
            page: create(PageResponseSchema, { nextCursor: 'c1' }),
          })
        : Promise.resolve({
            items: [
              { id: 'b', v: 2 },
              { id: 'c', v: 2 },
            ],
            page: create(PageResponseSchema, { nextCursor: '' }),
          })),
      {
        maxPages: 5,
        keyOf: x => x.id,
      },
    )
    expect(rows).toEqual([
      { id: 'a', v: 1 },
      { id: 'b', v: 2 },
      { id: 'c', v: 2 },
    ])
  })

  it('propagates a failed page read', async () => {
    await expect(fetchAllPages(
      async req => (req.cursor === ''
        ? page(['a'], 'c1')
        : Promise.reject(new Error('offline'))),
      {
        keyOf: x => x,
      },
    )).rejects.toThrow('offline')
  })

  it('carries the runaway guard that the listings share', () => {
    expect(MAX_PAGES).toBe(500)
  })
})
