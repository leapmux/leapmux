import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createWorkerOnlineStore } from './workerOnline.store'

describe('createWorkerOnlineStore', () => {
  it('reads a worker that nothing reported as online', () => {
    expect(createWorkerOnlineStore().isOnline('w1')).toBe(true)
  })

  it('reads a reported worker as offline until it comes back', () => {
    const store = createWorkerOnlineStore()

    expect(store.setOnline('w1', false)).toBe(true)
    expect(store.isOnline('w1')).toBe(false)
    expect(store.isOnline('w2'), 'another worker keeps its state').toBe(true)

    expect(store.setOnline('w1', true)).toBe(true)
    expect(store.isOnline('w1')).toBe(true)
    expect(store.offlineIds().size).toBe(0)
  })

  it('keeps the same set for a report of the current state', () => {
    const store = createWorkerOnlineStore()
    const empty = store.offlineIds()
    expect(store.setOnline('w1', true), 'an online worker reported online').toBe(false)
    expect(store.offlineIds()).toBe(empty)

    store.setOnline('w1', false)
    const offline = store.offlineIds()
    expect(store.setOnline('w1', false), 'an offline worker reported offline').toBe(false)
    expect(store.offlineIds()).toBe(offline)
  })

  it('keeps the empty id of a removed tab outside the set', () => {
    const store = createWorkerOnlineStore()

    expect(store.setOnline('', false)).toBe(false)

    expect(store.offlineIds().size).toBe(0)
    expect(store.isOnline('')).toBe(true)
  })

  it('tracks several workers at once', () => {
    const store = createWorkerOnlineStore()
    store.setOnline('w1', false)
    store.setOnline('w2', false)

    store.setOnline('w1', true)

    expect([
      ...store.offlineIds(),
    ]).toEqual(['w2'])
  })

  it('updates a reactive reader when the state changes, and only then', () => {
    const store = createWorkerOnlineStore()
    const seen: boolean[] = []
    const dispose = createRoot((d) => {
      createEffect(() => seen.push(store.isOnline('w1')))
      return d
    })

    store.setOnline('w1', true)
    store.setOnline('w1', false)
    store.setOnline('w1', false)
    store.setOnline('w1', true)

    expect(seen).toEqual([true, false, true])
    dispose()
  })
})
