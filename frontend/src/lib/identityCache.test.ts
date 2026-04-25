/// <reference types="vitest/globals" />
import { describe, expect, it, vi } from 'vitest'

import { createIdentityCache } from './identityCache'

interface Item {
  id: string
  name: string
}

describe('createIdentityCache', () => {
  it('returns the same reference when the same item is restabilized', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const a = { id: '1', name: 'a' }
    const [first] = cache.stabilize([a])
    const [second] = cache.stabilize([{ id: '1', name: 'a' }])
    expect(second).toBe(first)
  })

  it('returns a new reference when the content changes', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    const [second] = cache.stabilize([{ id: '1', name: 'b' }])
    expect(second).not.toBe(first)
    expect(second.name).toBe('b')
  })

  it('returns a new reference when the key changes even if content matches', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    const [second] = cache.stabilize([{ id: '2', name: 'a' }])
    expect(second).not.toBe(first)
    expect(second.id).toBe('2')
  })

  it('preserves input order', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const out = cache.stabilize([
      { id: 'b', name: 'b' },
      { id: 'a', name: 'a' },
      { id: 'c', name: 'c' },
    ])
    expect(out.map(i => i.id)).toEqual(['b', 'a', 'c'])
  })

  it('evicts items that disappear from the list', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    cache.stabilize([{ id: '2', name: 'b' }])
    const [readded] = cache.stabilize([{ id: '1', name: 'a' }])
    // After id:1 was evicted, restabilizing must return the new reference,
    // not the original cached one.
    expect(readded).not.toBe(first)
  })

  it('preserves identity for unchanged items while new items get fresh refs', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [a1, b1] = cache.stabilize([
      { id: 'a', name: 'a' },
      { id: 'b', name: 'b' },
    ])
    const out = cache.stabilize([
      { id: 'a', name: 'a' },
      { id: 'b', name: 'b-changed' },
      { id: 'c', name: 'c' },
    ])
    expect(out[0]).toBe(a1) // unchanged
    expect(out[1]).not.toBe(b1) // content changed
    expect(out[1].name).toBe('b-changed')
    expect(out[2].id).toBe('c') // brand new
  })

  it('handles an empty list by evicting everything', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    expect(cache.stabilize([])).toEqual([])
    const [readded] = cache.stabilize([{ id: '1', name: 'a' }])
    expect(readded).not.toBe(first)
  })

  it('clear() drops cached entries', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    cache.clear()
    const [next] = cache.stabilize([{ id: '1', name: 'a' }])
    expect(next).not.toBe(first)
  })

  it('does not mutate the input list', () => {
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    const input = [
      { id: 'a', name: 'a' },
      { id: 'b', name: 'b' },
    ]
    const snapshot = input.map(i => ({ ...i }))
    cache.stabilize(input)
    expect(input).toEqual(snapshot)
  })

  it('uses a custom equals function when provided', () => {
    // equals ignores the `name` field — only id matters for equality.
    const equals = vi.fn((cached: Item, fresh: Item) => cached.id === fresh.id)
    const cache = createIdentityCache<Item>({ keyOf: i => i.id, equals })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    const [second] = cache.stabilize([{ id: '1', name: 'b' }])
    expect(second).toBe(first)
    expect(equals).toHaveBeenCalled()
  })

  it('returns a new reference when custom equals returns false', () => {
    // equals always returns false → identity is never reused.
    const cache = createIdentityCache<Item>({
      keyOf: i => i.id,
      equals: () => false,
    })
    const [first] = cache.stabilize([{ id: '1', name: 'a' }])
    const [second] = cache.stabilize([{ id: '1', name: 'a' }])
    expect(second).not.toBe(first)
  })

  it('does not call equals when no prior entry exists for the key', () => {
    const equals = vi.fn(() => true)
    const cache = createIdentityCache<Item>({ keyOf: i => i.id, equals })
    cache.stabilize([{ id: '1', name: 'a' }])
    expect(equals).not.toHaveBeenCalled()
  })

  it('updates the cache to the fresh ref when content differs (default equals)', () => {
    // When the content changes we keep the new ref — and that ref must be
    // what comes back from the next round, not the one before it.
    const cache = createIdentityCache<Item>({ keyOf: i => i.id })
    cache.stabilize([{ id: '1', name: 'a' }])
    const [updated] = cache.stabilize([{ id: '1', name: 'b' }])
    const [next] = cache.stabilize([{ id: '1', name: 'b' }])
    expect(next).toBe(updated)
  })

  it('handles complex composite keys', () => {
    interface Pair { left: string, right: string, value: number }
    const cache = createIdentityCache<Pair>({
      keyOf: p => `${p.left}|${p.right}`,
    })
    const [first] = cache.stabilize([{ left: 'a', right: 'b', value: 1 }])
    const [same] = cache.stabilize([{ left: 'a', right: 'b', value: 1 }])
    const [diff] = cache.stabilize([{ left: 'a', right: 'c', value: 1 }])
    expect(same).toBe(first)
    expect(diff).not.toBe(first)
  })

  it('treats nested object refs as not-equal under default shallow equals', () => {
    interface Nested { id: string, payload: { v: number } }
    const cache = createIdentityCache<Nested>({ keyOf: i => i.id })
    const [first] = cache.stabilize([{ id: '1', payload: { v: 1 } }])
    const [second] = cache.stabilize([{ id: '1', payload: { v: 1 } }])
    // Different `payload` object reference — default shallowEqual returns false.
    expect(second).not.toBe(first)
  })
})
