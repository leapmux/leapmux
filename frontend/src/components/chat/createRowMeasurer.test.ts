import type { ResizeObserverLike } from './createRowMeasurer'
import { describe, expect, it, vi } from 'vitest'
import { createRowMeasurer } from './createRowMeasurer'

/** A fake row whose measured height and connectedness the test controls. */
function fakeEl(height: number, connected = true) {
  return {
    isConnected: connected,
    getBoundingClientRect: () => ({ height }),
  } as unknown as HTMLElement & { isConnected: boolean }
}

/** A fake ResizeObserver whose callback the test fires deterministically. */
function fakeObserver() {
  const observed = new Set<Element>()
  let onResize: ((targets: Element[]) => void) | undefined
  const createObserver = (cb: (targets: Element[]) => void): ResizeObserverLike => {
    onResize = cb
    return {
      observe: el => observed.add(el),
      unobserve: el => observed.delete(el),
      disconnect: () => observed.clear(),
    }
  }
  return { createObserver, observed, fire: (targets: Element[]) => onResize!(targets) }
}

/** A manual microtask scheduler: the test runs the queued flush when it chooses. */
function manualScheduler() {
  let queued: (() => void) | undefined
  let calls = 0
  return {
    scheduleMicrotask: (cb: () => void) => {
      calls++
      queued = cb
    },
    get calls() {
      return calls
    },
    run() {
      const f = queued
      queued = undefined
      f?.()
    },
  }
}

describe('createrowmeasurer', () => {
  it('measures and observes a freshly-mounted row, marking it mounted', () => {
    const measure = vi.fn(() => true)
    const mountedIds = new Set<string>()
    const obs = fakeObserver()
    const m = createRowMeasurer({ measure, mountedIds, createObserver: obs.createObserver })
    const el = fakeEl(120)
    m.attachRow('r1', el)
    expect(measure).toHaveBeenCalledWith('r1', 120)
    expect(mountedIds.has('r1')).toBe(true)
    expect(obs.observed.has(el)).toBe(true)
  })

  it('defers visible measurements while the gate is closed and flushes the latest live height', () => {
    let defer = true
    let height = 120
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const el = {
      isConnected: true,
      getBoundingClientRect: () => ({ height }),
    } as unknown as HTMLElement
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
      shouldDeferMeasurement: () => defer,
    })

    m.attachRow('r1', el)
    expect(measure).not.toHaveBeenCalled()
    expect(obs.observed.has(el)).toBe(true)
    height = 140
    obs.fire([el])
    sched.run()
    expect(measure).not.toHaveBeenCalled()

    defer = false
    expect(m.flushDeferredMeasurements()).toBe(true)
    expect(measure).toHaveBeenCalledTimes(1)
    expect(measure).toHaveBeenCalledWith('r1', 140)
  })

  it('coalesces resize ticks into ONE scheduled flush that commits each measurement', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    let aHeight = 100
    let bHeight = 200
    const a = {
      isConnected: true,
      getBoundingClientRect: () => ({ height: aHeight }),
    } as unknown as HTMLElement
    const b = {
      isConnected: true,
      getBoundingClientRect: () => ({ height: bHeight }),
    } as unknown as HTMLElement
    m.attachRow('a', a)
    m.attachRow('b', b)
    measure.mockClear()
    aHeight = 101
    bHeight = 201

    // Two resize ticks before the flush runs -> the flush is scheduled only ONCE
    // (dedup) and nothing commits until it runs.
    obs.fire([a])
    obs.fire([b])
    expect(sched.calls).toBe(1)
    expect(measure).not.toHaveBeenCalled()

    sched.run()
    expect(measure).toHaveBeenCalledWith('a', 101)
    expect(measure).toHaveBeenCalledWith('b', 201)
  })

  it('skips ResizeObserver measurements that are unchanged within epsilon', () => {
    const measure = vi.fn(() => true)
    const onFlush = vi.fn()
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
      onFlush,
    })
    const a = fakeEl(100)
    m.attachRow('a', a)
    measure.mockClear()

    obs.fire([a])
    sched.run()

    expect(measure).not.toHaveBeenCalled()
    expect(onFlush).toHaveBeenCalledWith({
      targets: 1,
      reads: 1,
      committed: 0,
      skippedDetached: 0,
      skippedUnchanged: 1,
    })
  })

  it('measures a reused element when it is reattached under a different row id', () => {
    let defer = false
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      currentMeasurementKey: () => 'same-key',
      shouldDeferMeasurement: () => defer,
    })
    const el = fakeEl(100)

    m.attachRow('old-row', el)
    measure.mockClear()
    defer = true
    m.attachRow('new-row', el)

    defer = false
    expect(m.flushDeferredMeasurements()).toBe(true)
    expect(measure).toHaveBeenCalledWith('new-row', 100)
  })

  it('re-commits unchanged DOM heights when the row measurement key changed', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    let key = 'old'
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
      currentMeasurementKey: () => key,
    })
    const a = fakeEl(100)
    m.attachRow('a', a)
    measure.mockClear()

    key = 'new'
    obs.fire([a])
    sched.run()

    expect(measure).toHaveBeenCalledWith('a', 100)
  })

  it('commits ResizeObserver measurements that move beyond epsilon', () => {
    let height = 100
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    const a = {
      isConnected: true,
      getBoundingClientRect: () => ({ height }),
    } as unknown as HTMLElement
    m.attachRow('a', a)
    measure.mockClear()

    height = 100.75
    obs.fire([a])
    sched.run()

    expect(measure).toHaveBeenCalledWith('a', 100.75)
  })

  it('skips a disconnected element during the flush', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    let aHeight = 100
    const a = {
      isConnected: true,
      getBoundingClientRect: () => ({ height: aHeight }),
    } as unknown as HTMLElement
    const b = fakeEl(200)
    m.attachRow('a', a)
    m.attachRow('b', b)
    measure.mockClear()
    aHeight = 101
    ;(b as { isConnected: boolean }).isConnected = false // b left the DOM before the flush

    obs.fire([a, b])
    sched.run()
    expect(measure).toHaveBeenCalledWith('a', 101)
    expect(measure).not.toHaveBeenCalledWith('b', 200)
  })

  it('detachRow unobserves, unmounts, and drops a pending measurement', () => {
    const measure = vi.fn(() => true)
    const mountedIds = new Set<string>()
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds,
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    const a = fakeEl(100)
    m.attachRow('a', a)
    obs.fire([a]) // a is now pending a measurement
    measure.mockClear()

    m.detachRow(a)
    expect(mountedIds.has('a')).toBe(false)
    expect(obs.observed.has(a)).toBe(false)

    sched.run() // the pending flush must not measure the detached row
    expect(measure).not.toHaveBeenCalled()
  })

  it('detachRow drops the el->id mapping so a late resize for it does not measure', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    const a = fakeEl(100)
    m.attachRow('a', a)
    m.detachRow(a)
    measure.mockClear()
    // An in-flight resize tick that still references the detached element: with the
    // el->id mapping dropped, the flush can't resolve an id and skips it.
    obs.fire([a])
    sched.run()
    expect(measure).not.toHaveBeenCalled()
  })

  it('keeps an id mounted when a NEW element re-claims it before the old element detaches', () => {
    // Attach-before-detach remount: the row remounts under a new element (attachRow)
    // before the old element's cleanup runs (detachRow). The id must stay mounted --
    // detaching the OLD element must not un-protect the freshly-mounted row.
    const measure = vi.fn(() => true)
    const mountedIds = new Set<string>()
    const obs = fakeObserver()
    const m = createRowMeasurer({ measure, mountedIds, createObserver: obs.createObserver })
    const oldEl = fakeEl(100)
    const newEl = fakeEl(120)
    m.attachRow('r1', oldEl)
    m.attachRow('r1', newEl) // new element claims r1 before oldEl's cleanup
    m.detachRow(oldEl) // oldEl's deferred cleanup -- must NOT unmount r1
    expect(mountedIds.has('r1')).toBe(true)
    expect(obs.observed.has(newEl)).toBe(true)
    // The newer element's own detach finally relinquishes the id.
    m.detachRow(newEl)
    expect(mountedIds.has('r1')).toBe(false)
  })

  it('skips a pending resize from an old connected element after a new element claims the same id', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    let oldHeight = 100
    const oldEl = {
      isConnected: true,
      getBoundingClientRect: () => ({ height: oldHeight }),
    } as unknown as HTMLElement
    const newEl = fakeEl(120)

    m.attachRow('r1', oldEl)
    oldHeight = 101
    obs.fire([oldEl])
    m.attachRow('r1', newEl)
    measure.mockClear()

    sched.run()

    expect(measure).not.toHaveBeenCalled()
  })

  it('dispose disconnects the observer', () => {
    const obs = fakeObserver()
    const m = createRowMeasurer({ measure: () => true, mountedIds: new Set(), createObserver: obs.createObserver })
    m.attachRow('a', fakeEl(100))
    expect(obs.observed.size).toBe(1)
    m.dispose()
    expect(obs.observed.size).toBe(0)
  })

  it('dispose resets the scheduled flag so a later tick can re-arm a flush', () => {
    const obs = fakeObserver()
    const sched = manualScheduler()
    const m = createRowMeasurer({
      measure: () => true,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: sched.scheduleMicrotask,
    })
    const a = fakeEl(100)
    m.attachRow('a', a)
    obs.fire([a]) // schedules a flush (flushScheduled = true)
    expect(sched.calls).toBe(1)
    m.dispose() // must clear flushScheduled, else scheduleFlush can never re-arm
    obs.fire([a])
    expect(sched.calls).toBe(2)
  })

  it('still measures immediately when no observer is available (non-DOM env)', () => {
    const measure = vi.fn(() => true)
    const m = createRowMeasurer({ measure, mountedIds: new Set(), createObserver: () => undefined })
    m.attachRow('a', fakeEl(140))
    expect(measure).toHaveBeenCalledWith('a', 140)
  })
})
