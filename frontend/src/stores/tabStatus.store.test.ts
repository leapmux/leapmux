import type { TabStatusView } from '~/lib/tabStatus'
import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createTabStatusStore } from './tabStatus.store'

const running: TabStatusView = { text: 'Running the tests', progressPercent: 40, tone: 'warning' }
const done: TabStatusView = { text: 'Done', progressPercent: undefined, tone: 'success' }

describe('createTabStatusStore', () => {
  it('holds no status for a tab that reported none', () => {
    expect(createTabStatusStore().get('a1')).toBeUndefined()
  })

  it('applies a status and replaces it whole', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    expect(store.get('a1')).toEqual(running)

    // The new status has no progress: a merge would keep the old 40.
    store.apply('w1', 'a1', done)
    expect(store.get('a1')).toEqual(done)
  })

  it('clears a status, and a clear of a tab with none is harmless', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a1', undefined)
    expect(store.get('a1')).toBeUndefined()
    store.apply('w1', 'never-set', undefined)
    expect(store.get('never-set')).toBeUndefined()
  })

  it('forgets the statuses of one worker and keeps the others', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a2', done)
    store.apply('w2', 'b1', running)

    store.forgetWorker('w1')
    expect(store.get('a1')).toBeUndefined()
    expect(store.get('a2')).toBeUndefined()
    expect(store.get('b1')).toEqual(running)
  })

  // The entry belongs to the Worker that reported it last, so the close of an
  // earlier Worker's stream cannot drop a status that another one still holds.
  it('keeps a status that a second worker replaced when the first worker is forgotten', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    store.apply('w2', 'a1', done)

    store.forgetWorker('w1')
    expect(store.get('a1')).toEqual(done)
    store.forgetWorker('w2')
    expect(store.get('a1')).toBeUndefined()
  })

  // dropWhere deletes every entry in one write, so a reader of several tabs
  // sees one change rather than one per tab.
  it('drops every status of a worker in one write', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a2', done)
    const seen: [string | undefined, string | undefined][] = []
    const dispose = createRoot((dispose) => {
      createEffect(() => seen.push([store.get('a1')?.text, store.get('a2')?.text]))
      return dispose
    })

    store.forgetWorker('w1')
    expect(seen).toEqual([
      ['Running the tests', 'Done'],
      [undefined, undefined],
    ])
    dispose()
  })

  it('drops the statuses of closed tabs and keeps the others', () => {
    const store = createTabStatusStore()
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a2', done)

    store.dropTabs(new Set(['a1', 'not-a-status']))
    expect(store.get('a1')).toBeUndefined()
    expect(store.get('a2')).toEqual(done)
    store.dropTabs(new Set())
    expect(store.get('a2')).toEqual(done)
  })

  it('notifies a reader of one tab when that tab\'s status changes', () => {
    const store = createTabStatusStore()
    const seen: (string | undefined)[] = []
    // Effects created in a root run once the root's own body returns.
    const dispose = createRoot((dispose) => {
      createEffect(() => seen.push(store.get('a1')?.text))
      return dispose
    })
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a1', done)
    store.forgetWorker('w1')
    expect(seen).toEqual([
      undefined,
      'Running the tests',
      'Done',
      undefined,
    ])
    dispose()
  })

  // The metadata sweep calls dropTabs from inside its effect. A tracked read of
  // every key would make that effect run again on each status change.
  it('does not subscribe the caller of dropTabs or forgetWorker to the store', () => {
    const store = createTabStatusStore()
    let runs = 0
    const dispose = createRoot((dispose) => {
      createEffect(() => {
        runs++
        store.dropTabs(new Set(['gone']))
        store.forgetWorker('w-gone')
      })
      return dispose
    })
    expect(runs).toBe(1)
    store.apply('w1', 'a1', running)
    store.apply('w1', 'a1', undefined)
    expect(runs).toBe(1)
    dispose()
  })
})
