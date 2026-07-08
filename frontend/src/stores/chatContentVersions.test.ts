import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createContentVersionStore } from '~/stores/chatContentVersions'

describe('chatcontentversions', () => {
  it('defaults to 0 and increments on each bump', () => {
    createRoot((dispose) => {
      const store = createContentVersionStore()
      expect(store.get('m1')).toBe(0)
      store.bump('m1')
      expect(store.get('m1')).toBe(1)
      store.bump('m1')
      expect(store.get('m1')).toBe(2)
      expect(store.get('m2')).toBe(0) // untouched rows stay 0
      dispose()
    })
  })

  it('forget drops the bumped rows (guarded: never-bumped ids are a no-op)', () => {
    createRoot((dispose) => {
      const store = createContentVersionStore()
      store.bump('a')
      store.bump('b')
      // 'c' was never bumped -- clearing it is a guarded no-op.
      store.forget(['a', 'c'])
      expect(store.get('a')).toBe(0) // dropped -> back to default
      expect(store.get('b')).toBe(1) // b untouched
      dispose()
    })
  })
})
