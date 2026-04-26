import { describe, expect, it } from 'vitest'
import { codexWebSearchActionFromItem } from './webSearch'

describe('codexWebSearchActionFromItem', () => {
  it('returns null for null/undefined', () => {
    expect(codexWebSearchActionFromItem(null)).toBeNull()
    expect(codexWebSearchActionFromItem(undefined)).toBeNull()
  })

  it('extracts an openPage action', () => {
    expect(codexWebSearchActionFromItem({
      action: { type: 'openPage', url: 'https://example.com/x' },
    })).toEqual({ type: 'openPage', url: 'https://example.com/x' })
  })

  it('extracts a findInPage action', () => {
    expect(codexWebSearchActionFromItem({
      action: { type: 'findInPage', pattern: 'foo', url: 'https://example.com' },
    })).toEqual({ type: 'findInPage', pattern: 'foo', url: 'https://example.com' })
  })

  it('extracts a search action with deduplicated queries', () => {
    expect(codexWebSearchActionFromItem({
      action: {
        type: 'search',
        query: 'top query',
        queries: ['top query', 'second query', 'second query'],
      },
    })).toEqual({
      type: 'search',
      query: 'top query',
      queries: ['top query', 'second query'],
    })
  })

  it('falls back to query when search action has no queries', () => {
    expect(codexWebSearchActionFromItem({
      action: { type: 'search' },
      query: 'fallback query',
    })).toEqual({
      type: 'search',
      query: 'fallback query',
      queries: [],
    })
  })

  it('returns "other" with item.query when no action is present', () => {
    expect(codexWebSearchActionFromItem({ query: 'just a query' })).toEqual({
      type: 'other',
      query: 'just a query',
    })
  })

  it('returns "other" with empty query for blank action.type and no query', () => {
    expect(codexWebSearchActionFromItem({})).toEqual({ type: 'other', query: '' })
  })
})
