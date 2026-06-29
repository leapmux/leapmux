import { describe, expect, it, vi } from 'vitest'
import {
  cachedRenderValue,
  cachedRenderValueForString,
  cachedRenderValueForStrings,
  createMessageRenderCacheStore,
  stableStringCacheKey,
} from './messageRenderCache'

describe('messageRenderCache', () => {
  it('reuses values within one row-version cache', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const compute = vi.fn(() => ({ value: 1 }))

    const first = cachedRenderValue({ renderCache: cache }, 'derived', compute)
    const second = cachedRenderValue({ renderCache: cache }, 'derived', compute)

    expect(first).toBe(second)
    expect(compute).toHaveBeenCalledTimes(1)
  })

  it('supports cache peeks without computing a missing value', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')

    expect(cache.get<string>('highlighted')).toBeUndefined()
    expect(cache.set('highlighted', '<pre>done</pre>')).toBe('<pre>done</pre>')
    expect(cache.get<string>('highlighted')).toBe('<pre>done</pre>')
  })

  it('isolates row versions and evicts least-recent rows past the cap', () => {
    const store = createMessageRenderCacheStore(2)
    store.forRow('row:1').getOrCreate('x', () => 1)
    store.forRow('row:2').getOrCreate('x', () => 2)
    store.forRow('row:1').getOrCreate('x', () => 10)
    store.forRow('row:3').getOrCreate('x', () => 3)

    expect(store.size()).toBe(2)
    expect(store.forRow('row:1').getOrCreate('x', () => 10)).toBe(1)
    expect(store.forRow('row:2').getOrCreate('x', () => 20)).toBe(20)
  })

  it('prunes rows outside the live window', () => {
    const store = createMessageRenderCacheStore()
    store.forRow('row:1').getOrCreate('x', () => 1)
    store.forRow('row:2').getOrCreate('x', () => 2)

    store.prune(['row:2'])

    expect(store.size()).toBe(1)
    expect(store.forRow('row:1').getOrCreate('x', () => 10)).toBe(10)
    expect(store.forRow('row:2').getOrCreate('x', () => 20)).toBe(2)
  })

  it('builds stable string-derived keys without embedding large text', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:1')
    const text = 'same markdown body'
    const compute = vi.fn(() => '<p>same markdown body</p>')

    expect(stableStringCacheKey('markdown', text)).toBe(stableStringCacheKey('markdown', text))
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', text, compute)).toBe('<p>same markdown body</p>')
    expect(cachedRenderValueForString({ renderCache: cache }, 'markdown', text, compute)).toBe('<p>same markdown body</p>')
    expect(compute).toHaveBeenCalledTimes(1)
    expect(stableStringCacheKey('markdown', `${text}!`)).not.toBe(stableStringCacheKey('markdown', text))
  })

  it('does not reuse a string value when two inputs collide on the compact key', () => {
    const store = createMessageRenderCacheStore()
    const cache = store.forRow('row:collision')
    const first = 'wh7lwUUg'
    const second = 'zebMWNKb'
    const computeFirst = vi.fn(() => 'first-render')
    const computeSecond = vi.fn(() => 'second-render')

    expect(stableStringCacheKey('markdown', first)).toBe(stableStringCacheKey('markdown', second))
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

    expect(cachedRenderValueForStrings({ renderCache: cache }, 'diff', ['path', first, 'new'], () => 'first-diff')).toBe('first-diff')
    expect(cachedRenderValueForStrings({ renderCache: cache }, 'diff', ['path', second, 'new'], () => 'second-diff')).toBe('second-diff')
  })
})
