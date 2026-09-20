import { describe, expect, it, vi } from 'vitest'
import {
  cachedRenderValueForString,
  cachedRenderValueForStrings,
  createMessageRenderCacheStore,
  fixedCacheKey,
  stringCacheKey,
  stringTupleCacheKey,
} from './messageRenderCache'

describe('messageRenderCache', () => {
  it('reuses values within one row-version cache', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const key = fixedCacheKey<{ value: number }>('derived')
    const compute = vi.fn(() => ({ value: 1 }))

    const first = cache.getOrCreate(key, compute)
    const second = cache.getOrCreate(key, compute)

    expect(first).toBe(second)
    expect(compute).toHaveBeenCalledTimes(1)
  })

  it('supports cache peeks without computing a missing value', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const highlighted = fixedCacheKey<string>('highlighted')

    expect(cache.get(highlighted)).toBeUndefined()
    expect(cache.set(highlighted, '<pre>done</pre>')).toBe('<pre>done</pre>')
    expect(cache.get(highlighted)).toBe('<pre>done</pre>')
  })

  it('keys of different entry types do not address one another', () => {
    // The typed key is what keeps a read from answering an entry another kind of
    // value wrote: the two tokens are equal here on purpose, and the types still
    // refuse the swap at compile time. The runtime half this pins is that the
    // cache stores by token alone -- two keys, two tokens, no cross-talk even when
    // the names look alike.
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const html = fixedCacheKey<string>('body.html')
    const rows = fixedCacheKey<number>('body.rows')

    cache.set(html, '<p>one</p>')
    cache.set(rows, 3)

    expect(cache.get(html)).toBe('<p>one</p>')
    expect(cache.get(rows)).toBe(3)
  })

  it('isolates row versions and evicts least-recent rows past the cap', () => {
    const store = createMessageRenderCacheStore(2)
    const key = fixedCacheKey<number>('x')
    store.forRow('row:1').getOrCreate(key, () => 1)
    store.forRow('row:2').getOrCreate(key, () => 2)
    store.forRow('row:1').getOrCreate(key, () => 10)
    store.forRow('row:3').getOrCreate(key, () => 3)

    expect(store.size()).toBe(2)
    expect(store.forRow('row:1').getOrCreate(key, () => 10)).toBe(1)
    expect(store.forRow('row:2').getOrCreate(key, () => 20)).toBe(20)
  })

  it('prunes rows outside the live window', () => {
    const store = createMessageRenderCacheStore()
    const key = fixedCacheKey<number>('x')
    store.forRow('row:1').getOrCreate(key, () => 1)
    store.forRow('row:2').getOrCreate(key, () => 2)

    store.prune(['row:2'])

    expect(store.size()).toBe(1)
    expect(store.forRow('row:1').getOrCreate(key, () => 10)).toBe(10)
    expect(store.forRow('row:2').getOrCreate(key, () => 20)).toBe(2)
  })

  it('builds stable string-derived keys without embedding large text', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const text = 'same markdown body'
    const compute = vi.fn(() => '<p>same markdown body</p>')

    expect(stringCacheKey<string>('markdown', text).token).toBe(stringCacheKey<string>('markdown', text).token)
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', text, compute)).toBe('<p>same markdown body</p>')
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', text, compute)).toBe('<p>same markdown body</p>')
    expect(compute).toHaveBeenCalledTimes(1)
    expect(stringCacheKey<string>('markdown', `${text}!`).token).not.toBe(stringCacheKey<string>('markdown', text).token)
    // The token carries the digest, never the body itself: a streaming row's text
    // must not sit in the key as well as the entry.
    expect(stringCacheKey<string>('markdown', text).token).not.toContain(text)
  })

  it('does not reuse a string value when two inputs collide on the compact key', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:collision')
    const first = 'wh7lwUUg'
    const second = 'zebMWNKb'
    const computeFirst = vi.fn(() => 'first-render')
    const computeSecond = vi.fn(() => 'second-render')

    expect(stringCacheKey<string>('markdown', first).token).toBe(stringCacheKey<string>('markdown', second).token)
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', first, computeFirst)).toBe('first-render')
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', second, computeSecond)).toBe('second-render')

    expect(computeFirst).toHaveBeenCalledTimes(1)
    expect(computeSecond).toHaveBeenCalledTimes(1)
  })

  it('does not reuse tuple-string values when one tuple part collides', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:tuple-collision')
    const first = 'wh7lwUUg'
    const second = 'zebMWNKb'

    expect(stringTupleCacheKey<string>('diff', ['path', first, 'new']).token).toBe(stringTupleCacheKey<string>('diff', ['path', second, 'new']).token)
    expect(cachedRenderValueForStrings({ renderCache: cache }, 'diff', ['path', first, 'new'], () => 'first-diff')).toBe('first-diff')
    expect(cachedRenderValueForStrings({ renderCache: cache }, 'diff', ['path', second, 'new'], () => 'second-diff')).toBe('second-diff')
  })

  it('clear drops every row, which prune cannot', () => {
    // The two differ by WHICH invalidation they answer. `prune` drops rows that
    // left the list; a syntax theme change invalidates the output of every row
    // that is still ON the list, because each cached body carries Shiki's baked
    // token colours. The callers fold the theme generation into their key, so
    // without `clear` the old generation's entries were merely orphaned inside
    // each live row's map -- which nothing bounds by key count.
    const store = createMessageRenderCacheStore()
    const html = fixedCacheKey<string>('markdown-html:1')
    store.forRow('row-1').set(html, '<p>one</p>')
    store.forRow('row-2').set(html, '<p>two</p>')
    expect(store.size()).toBe(2)

    // `prune` keeps both, because both rows are live.
    store.prune(['row-1', 'row-2'])
    expect(store.size()).toBe(2)
    expect(store.forRow('row-1').get(html)).toBe('<p>one</p>')

    store.clear()
    expect(store.size()).toBe(0)
    expect(store.forRow('row-1').get(html)).toBeUndefined()
  })
})
