import { describe, expect, it } from 'vitest'
import { capMapInsertionOrder, lruGet, lruSet } from './mapLru'

describe('capmapinsertionorder', () => {
  it('returns the same map untouched while within the cap', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2]])
    const result = capMapInsertionOrder(map, 5)
    expect(result).toBe(map)
    expect(map.size).toBe(2)
    expect([...map.keys()]).toEqual(['a', 'b'])
  })

  it('drops insertion-order-oldest entries once over the cap', () => {
    const map = new Map<string, number>()
    for (let i = 0; i < 10; i++)
      map.set(`k${i}`, i)
    capMapInsertionOrder(map, 4)
    expect(map.size).toBe(4)
    expect(map.has('k5')).toBe(false) // oldest 6 evicted (10 - 4)
    expect(map.has('k6')).toBe(true) // oldest survivor
    expect(map.has('k9')).toBe(true) // newest kept
  })

  it('never evicts a protected key even when it is the oldest', () => {
    const map = new Map<string, number>()
    for (let i = 0; i < 6; i++)
      map.set(`k${i}`, i)
    // k0 is the oldest but protected; eviction should skip it and drop the next
    // oldest unprotected keys instead.
    capMapInsertionOrder(map, 3, { protect: new Set(['k0']) })
    expect(map.size).toBe(3)
    expect(map.has('k0')).toBe(true) // protected, kept despite being oldest
    expect(map.has('k1')).toBe(false) // next oldest, evicted
    expect(map.has('k2')).toBe(false)
    expect(map.has('k5')).toBe(true)
  })

  it('invokes onEvict for each dropped key before deleting it', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2], ['c', 3]])
    const seen: Array<[string, number | undefined]> = []
    capMapInsertionOrder(map, 1, {
      onEvict: (key) => { seen.push([key, map.get(key)]) }, // value still present at callback time
    })
    expect(seen).toEqual([['a', 1], ['b', 2]])
    expect([...map.keys()]).toEqual(['c'])
  })

  it('raises the cap to the protect-set size when every key is protected', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2], ['c', 3]])
    // All keys protected: the effective cap rises to max(1, 3) = 3, so the map is
    // already within bound and returned untouched -- a protected (on-screen) row is
    // never evicted from the cache that sizes it.
    capMapInsertionOrder(map, 1, { protect: new Set(['a', 'b', 'c']) })
    expect(map.size).toBe(3)
  })

  it('evicts non-protected keys down to the protect-set floor when protect exceeds max', () => {
    const map = new Map<string, number>()
    for (const k of ['a', 'b', 'c', 'd', 'e'])
      map.set(k, 0)
    // max=2 but 3 keys are protected: effective cap is max(2, 3) = 3. The two
    // non-protected keys are evicted, landing exactly at the protect-set floor (3) --
    // never above the effective bound, and never dropping a protected key.
    capMapInsertionOrder(map, 2, { protect: new Set(['a', 'b', 'c']) })
    expect(map.size).toBe(3)
    expect([...map.keys()]).toEqual(['a', 'b', 'c'])
  })
})

describe('lruget', () => {
  it('returns undefined and leaves the map untouched on a miss', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2]])
    expect(lruGet(map, 'z')).toBeUndefined()
    expect([...map.keys()]).toEqual(['a', 'b'])
  })

  it('re-fronts a hit to the most-recently-used end', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2], ['c', 3]])
    expect(lruGet(map, 'a')).toBe(1)
    // 'a' moves to the end; a subsequent over-cap trim now sheds 'b' (oldest) first.
    expect([...map.keys()]).toEqual(['b', 'c', 'a'])
  })

  it('treats a legitimately-stored undefined value as a hit (re-fronts it)', () => {
    const map = new Map<string, number | undefined>([['a', undefined], ['b', 2]])
    expect(lruGet(map, 'a')).toBeUndefined()
    expect([...map.keys()]).toEqual(['b', 'a']) // moved, not treated as a miss
  })
})

describe('lruset', () => {
  it('inserts a new key at the MRU end and caps oldest-first', () => {
    const map = new Map<string, number>()
    for (let i = 0; i < 4; i++)
      lruSet(map, `k${i}`, i, 3)
    expect(map.size).toBe(3)
    expect([...map.keys()]).toEqual(['k1', 'k2', 'k3']) // k0 (oldest) shed
  })

  it('re-fronts an overwritten key instead of dropping an unrelated live entry', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2], ['c', 3]])
    // Overwrite the oldest key at capacity: size never grows, so nothing is evicted,
    // and 'a' moves to the MRU end (a bare set would have kept it at the front).
    lruSet(map, 'a', 10, 3)
    expect(map.size).toBe(3)
    expect([...map.entries()]).toEqual([['b', 2], ['c', 3], ['a', 10]])
  })

  it('forwards protect/onEvict to the cap', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2]])
    const evicted: string[] = []
    lruSet(map, 'c', 3, 2, { protect: new Set(['a']), onEvict: k => evicted.push(k) })
    // After inserting 'c' (size 3) the cap (2) trims one: 'a' is protected and 'b' is
    // the oldest un-protected key, so 'b' is evicted while 'a' and the new 'c' survive.
    expect(evicted).toEqual(['b'])
    expect([...map.keys()]).toEqual(['a', 'c'])
  })
})
