import type { PageRequestInit } from './fetchAllPages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { PageResponseSchema } from '~/generated/proto/leapmux/v1/common_pb'
import { fetchAllPages, MAX_PAGES, PAGE_SIZE } from './fetchAllPages'

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
      { limit: PAGE_SIZE, cursor: '' },
      { limit: PAGE_SIZE, cursor: 'c1' },
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

  it('stops when the cursor fails to advance instead of looping', async () => {
    let calls = 0
    const rows = await fetchAllPages(
      async () => {
        calls++
        return page(['a'], 'stuck')
      },
      {
        maxPages: 10,
        keyOf: x => x,
      },
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
      {
        maxPages: 3,
        keyOf: x => x,
      },
    )
    expect(calls).toBe(3)
    expect(rows).toEqual(['row-1', 'row-2', 'row-3'])
  })

  // The guard counts pages read, so a limit of zero or below reads none.
  it('reads no page when maxPages is zero or negative', async () => {
    for (const maxPages of [0, -1]) {
      const fetchPage = vi.fn(async () => page(['a'], ''))
      const rows = await fetchAllPages(fetchPage, {
        maxPages,
        keyOf: x => x,
      })
      expect(fetchPage, String(maxPages)).not.toHaveBeenCalled()
      expect(rows, String(maxPages)).toEqual([])
    }
  })

  it('stops at MAX_PAGES when the caller gives no maxPages', async () => {
    let calls = 0
    await fetchAllPages(
      async () => {
        calls++
        return page([`row-${calls}`], `c${calls}`)
      },
      {
        keyOf: x => x,
      },
    )
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

  it('carries the runaway-guard constants the listings share', () => {
    expect(PAGE_SIZE).toBe(500)
    expect(MAX_PAGES).toBe(500)
  })
})
