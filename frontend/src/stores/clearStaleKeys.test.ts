import { createRoot } from 'solid-js'
import { createStore } from 'solid-js/store'
import { describe, expect, it } from 'vitest'
import { clearStaleKeys } from '~/stores/clearStaleKeys'

describe('clearStaleKeys', () => {
  it('deletes the present keys and leaves the rest, in one batched update', () => {
    createRoot((dispose) => {
      const [map, setMap] = createStore<Record<string, number>>({ a: 1, b: 2, c: 3 })
      clearStaleKeys(map, setMap, ['a', 'c'])
      expect(map.a).toBeUndefined()
      expect(map.c).toBeUndefined()
      expect(map.b).toBe(2)
      expect('a' in map).toBe(false)
      dispose()
    })
  })

  it('skips ids that are not present (the common drop of absent rows is a no-op)', () => {
    createRoot((dispose) => {
      const [map, setMap] = createStore<Record<string, string>>({ x: 'v' })
      // None of these ids carry a value, so nothing is written.
      clearStaleKeys(map, setMap, ['p', 'q', 'r'])
      expect(map.x).toBe('v')
      expect(Object.keys(map)).toEqual(['x'])
      dispose()
    })
  })

  it('clears only the present subset when ids mix present and absent', () => {
    createRoot((dispose) => {
      const [map, setMap] = createStore<Record<string, number>>({ a: 1, b: 2 })
      clearStaleKeys(map, setMap, ['a', 'ghost'])
      expect(map.a).toBeUndefined()
      expect(map.b).toBe(2)
      expect('ghost' in map).toBe(false)
      dispose()
    })
  })
})
