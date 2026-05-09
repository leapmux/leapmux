import { describe, expect, it } from 'vitest'
import { createActiveClientStore } from './activeClient'

describe('activeClientStore', () => {
  it('returns "" for an unknown workspace', () => {
    const store = createActiveClientStore()
    expect(store.activeFor('w1')).toBe('')
  })

  it('update sets the active client and activeFor returns it', () => {
    const store = createActiveClientStore()
    store.update('w1', 'clientA')
    expect(store.activeFor('w1')).toBe('clientA')
    expect(store.activeFor('w2')).toBe('')
  })

  it('update with the same value is a no-op (does not bump the signal)', () => {
    const store = createActiveClientStore()
    store.update('w1', 'clientA')
    const before = store.snapshot()
    store.update('w1', 'clientA')
    expect(store.snapshot()).toBe(before)
  })

  it('update with empty active id removes the entry', () => {
    const store = createActiveClientStore()
    store.update('w1', 'clientA')
    store.update('w1', '')
    expect(store.activeFor('w1')).toBe('')
    expect(store.snapshot().has('w1')).toBe(false)
  })

  it('clear drops every entry', () => {
    const store = createActiveClientStore()
    store.update('w1', 'clientA')
    store.update('w2', 'clientB')
    store.clear()
    expect(store.snapshot().size).toBe(0)
  })
})
