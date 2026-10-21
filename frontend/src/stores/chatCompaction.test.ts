import { createRenderEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createLiveCompactionStore } from './chatCompaction'

describe('createLiveCompactionStore', () => {
  it('sets and clears a compaction without changing another agent', () => {
    const store = createLiveCompactionStore()
    expect(store.get('a')).toBeUndefined()
    store.set('a', { trigger: 'auto' })
    store.set('b', {})
    store.clear('b')
    store.clear('b')
    expect(store.get('a')).toEqual({ trigger: 'auto' })
    expect(store.get('b')).toBeUndefined()
  })

  it('replaces every field without changing the caller object', () => {
    const store = createLiveCompactionStore()
    const first = { trigger: 'manual' }
    store.set('a', first)
    store.set('a', {})
    expect(store.get('a')).toEqual({})
    expect(first).toEqual({ trigger: 'manual' })
  })

  it('notifies a reader when the trigger leaves', () => {
    const store = createLiveCompactionStore()
    const seen: (string | undefined)[] = []
    store.set('a', { trigger: 'auto' })
    const dispose = createRoot((disposeRoot) => {
      createRenderEffect(() => seen.push(store.get('a')?.trigger))
      return disposeRoot
    })
    store.set('a', {})
    expect(seen).toEqual(['auto', undefined])
    dispose()
  })
})
