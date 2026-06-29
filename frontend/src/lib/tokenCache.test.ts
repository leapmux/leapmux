import { beforeEach, describe, expect, it } from 'vitest'
import { _resetTokenCache, _TOKEN_CACHE_MAX_SIZE, getCachedTokens, makeKey, setCachedTokens, toCachedTokens } from './tokenCache'

describe('makeKey', () => {
  // The NUL separator, built via fromCharCode so this source file stays plain ASCII.
  const NUL = String.fromCharCode(0)

  it('joins lang and code with a NUL separator', () => {
    expect(makeKey('typescript', 'const x = 1')).toBe(`typescript${NUL}const x = 1`)
  })

  it('is collision-free across the lang/code boundary (why the NUL separator matters)', () => {
    // A naive `lang + code` join would collide: ('ab','c') and ('a','bc') both -> 'abc'.
    // NUL can't appear in a Shiki language id, so the split point is unambiguous.
    expect(makeKey('ab', 'c')).not.toBe(makeKey('a', 'bc'))
  })

  it('is the exact key the cache stores under (single-sourced across consumers)', () => {
    _resetTokenCache()
    const tokens = toCachedTokens([[{ content: 'x' }]])
    setCachedTokens('json', '{"a":1}', tokens)
    // A caller that rebuilds the key via makeKey addresses the same entry the cache wrote.
    expect(makeKey('json', '{"a":1}')).toBe(`json${NUL}{"a":1}`)
    expect(getCachedTokens('json', '{"a":1}')).toEqual(tokens)
  })
})

describe('toCachedTokens', () => {
  it('projects each token to only content + htmlStyle, dropping other fields', () => {
    // Shiki ThemedTokens carry extra fields (offset, color, fontStyle, ...) that are
    // not structured-clone-friendly to ship across the worker boundary and that the
    // renderer never reads. The projection must keep ONLY content + htmlStyle.
    const lines = [
      [
        { content: 'const', htmlStyle: { '--shiki-light': '#abc' }, offset: 0, color: '#abc', fontStyle: 1 },
        { content: ' x', htmlStyle: '--shiki-light:#def', offset: 5 },
      ],
      [],
    ]
    const result = toCachedTokens(lines as any)
    expect(result).toEqual([
      [
        { content: 'const', htmlStyle: { '--shiki-light': '#abc' } },
        { content: ' x', htmlStyle: '--shiki-light:#def' },
      ],
      [],
    ])
    // The extra fields are gone, not merely undefined.
    expect(Object.keys(result[0][0])).toEqual(['content', 'htmlStyle'])
  })

  it('preserves a missing htmlStyle as undefined', () => {
    const result = toCachedTokens([[{ content: 'x' }]])
    expect(result).toEqual([[{ content: 'x', htmlStyle: undefined }]])
  })

  it('round-trips through the LRU cache', () => {
    const tokens = toCachedTokens([[{ content: 'a', htmlStyle: '--shiki-light:#1' }]])
    setCachedTokens('json', '{"a":1}', tokens)
    expect(getCachedTokens('json', '{"a":1}')).toEqual(tokens)
    expect(getCachedTokens('json', 'other')).toBeUndefined()
  })
})

describe('token cache LRU eviction', () => {
  const tok = (s: string) => toCachedTokens([[{ content: s }]])

  beforeEach(() => {
    _resetTokenCache()
  })

  it('evicts the oldest entry once the capacity bound is exceeded', () => {
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    // One past capacity evicts the oldest (c-0).
    setCachedTokens('json', 'c-overflow', tok('x'))
    expect(getCachedTokens('json', 'c-0')).toBeUndefined()
    expect(getCachedTokens('json', 'c-overflow')).toEqual(tok('x'))
  })

  it('overwriting an existing key at capacity evicts nothing', () => {
    // Regression for the bespoke "evict-before-set" path: overwriting a key that already
    // exists doesn't grow the map, so the bound isn't exceeded and no live entry should
    // be dropped. The old code evicted the oldest unconditionally on any set at capacity.
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    setCachedTokens('json', 'c-10', tok('updated')) // overwrite, no read first (a read reorders)
    expect(getCachedTokens('json', 'c-0')).toBeDefined() // oldest survived
    expect(getCachedTokens('json', 'c-10')).toEqual(tok('updated'))
  })

  it('a cache read refreshes LRU position so the touched entry outlives eviction', () => {
    for (let i = 0; i < _TOKEN_CACHE_MAX_SIZE; i++)
      setCachedTokens('json', `c-${i}`, tok(String(i)))
    // Touch the oldest -> it becomes most-recently-used.
    expect(getCachedTokens('json', 'c-0')).toBeDefined()
    // A fresh insert now evicts the NEXT-oldest (c-1), not the refreshed c-0.
    setCachedTokens('json', 'c-fresh', tok('y'))
    expect(getCachedTokens('json', 'c-0')).toBeDefined()
    expect(getCachedTokens('json', 'c-1')).toBeUndefined()
  })
})
