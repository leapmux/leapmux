import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createStreamingTextStore } from '~/stores/chatStreamingText'

describe('chatStreamingText', () => {
  it('returns the empty string for an agent with no buffer', () =>
    createRoot((dispose) => {
      const store = createStreamingTextStore()
      expect(store.get('a1')).toBe('')
      dispose()
    }))

  it('sets and reads a per-agent buffer', () =>
    createRoot((dispose) => {
      const store = createStreamingTextStore()
      store.set('a1', 'hello')
      store.set('a2', 'world')
      expect(store.get('a1')).toBe('hello')
      expect(store.get('a2')).toBe('world')
      store.clear('a1')
      expect(store.get('a1')).toBe('')
      expect(store.get('a2')).toBe('world')
      dispose()
    }))

  it('clearing an already-empty buffer is a safe no-op (skip branch)', () =>
    createRoot((dispose) => {
      const store = createStreamingTextStore()
      store.clear('a1') // never set -> hits the empty-skip branch
      expect(store.get('a1')).toBe('')
      store.set('a1', 'x')
      store.clear('a1') // non-empty -> real clear
      store.clear('a1') // now empty -> skip branch again
      expect(store.get('a1')).toBe('')
      dispose()
    }))
})
