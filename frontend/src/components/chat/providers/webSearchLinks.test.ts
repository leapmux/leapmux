import { describe, expect, it } from 'vitest'
import { extractWebSearchLinks } from './webSearchLinks'

describe('extractWebSearchLinks', () => {
  it('deduplicates trimmed urls in order, keeps shared titles on distinct urls, and falls back to url', () => {
    expect(extractWebSearchLinks([
      { title: '  Docs  ', url: '  https://example.com/a  ' },
      { title: 'Duplicate', url: 'https://example.com/a' },
      { title: 'Docs', url: 'https://example.com/b' },
      { title: '   ', url: 'https://example.com/c' },
      { url: 'https://example.com/d' },
    ])).toEqual([
      { title: 'Docs', url: 'https://example.com/a' },
      { title: 'Docs', url: 'https://example.com/b' },
      { title: 'https://example.com/c', url: 'https://example.com/c' },
      { title: 'https://example.com/d', url: 'https://example.com/d' },
    ])
  })

  it.each([
    undefined,
    null,
    'invalid',
    42,
    {},
  ])('returns an empty list for non-array input %j', (entries) => {
    expect(extractWebSearchLinks(entries)).toEqual([])
  })

  it('skips non-object items and entries with missing or blank urls', () => {
    expect(extractWebSearchLinks([
      null,
      0,
      'text',
      { title: 'Missing URL' },
      { title: 'Blank URL', url: '   ' },
      { title: 'Valid', url: 'https://example.com/valid' },
    ])).toEqual([
      { title: 'Valid', url: 'https://example.com/valid' },
    ])
  })
})
