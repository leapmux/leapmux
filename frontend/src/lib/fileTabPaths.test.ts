import { describe, expect, it } from 'vitest'
import { createFileTabPathsStore } from './fileTabPaths'

describe('fileTabPathsStore', () => {
  it('register/pathFor round-trips a path', () => {
    const store = createFileTabPathsStore()
    expect(store.pathFor('t1')).toBeUndefined()
    store.register('t1', '/repo/a.go')
    expect(store.pathFor('t1')).toBe('/repo/a.go')
  })

  it('register is idempotent for an identical path', () => {
    const store = createFileTabPathsStore()
    store.register('t1', '/x')
    const before = store.snapshot()
    store.register('t1', '/x')
    // Same map reference because no change.
    expect(store.snapshot()).toBe(before)
  })

  it('register replaces the entry when the path changes', () => {
    const store = createFileTabPathsStore()
    store.register('t1', '/x')
    store.register('t1', '/y')
    expect(store.pathFor('t1')).toBe('/y')
  })

  it('revoke deletes the entry', () => {
    const store = createFileTabPathsStore()
    store.register('t1', '/x')
    store.revoke('t1')
    expect(store.pathFor('t1')).toBeUndefined()
  })

  it('revoke for an absent tab is a no-op', () => {
    const store = createFileTabPathsStore()
    store.register('t1', '/x')
    const before = store.snapshot()
    store.revoke('ghost')
    expect(store.snapshot()).toBe(before)
  })

  it('clear drops every entry', () => {
    const store = createFileTabPathsStore()
    store.register('t1', '/x')
    store.register('t2', '/y')
    store.clear()
    expect(store.snapshot().size).toBe(0)
  })
})
