import type { ResizeObserverLike } from './createRowMeasurer'
import { describe, expect, it, vi } from 'vitest'
import { createRowMeasurer } from './createRowMeasurer'

function fakeRowEvents() {
  const target = new EventTarget()
  return {
    addEventListener: target.addEventListener.bind(target),
    removeEventListener: target.removeEventListener.bind(target),
  }
}

/** A fake row whose measured height and connectedness the test controls. */
function fakeEl(height: number, connected = true) {
  return {
    ...fakeRowEvents(),
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

describe('createRowMeasurer', () => {
  it.each(['load', 'error'])('retries an empty row on image %s without a resize event', (eventName) => {
    const element = document.createElement('div')
    const image = document.createElement('img')
    let complete = false
    Object.defineProperty(image, 'complete', { get: () => complete })
    element.append(image)
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(element)
    const measure = vi.fn(() => true)
    const scheduler = manualScheduler()
    const measurer = createRowMeasurer({ measure, mountedIds: new Set(), scheduleMicrotask: scheduler.scheduleMicrotask, createObserver: () => undefined })
    try {
      measurer.attachRow('empty', element)
      expect(measure).not.toHaveBeenCalled()
      complete = true
      image.dispatchEvent(new Event(eventName))
      scheduler.run()
      expect(measure).toHaveBeenCalledWith('empty', 0)
    }
    finally {
      measurer.dispose()
      element.remove()
    }
  })

  it('waits for the last image and ignores a non-image load event', () => {
    const element = document.createElement('div')
    const images = [document.createElement('img'), document.createElement('img')]
    const complete = [false, false]
    images.forEach((image, index) => {
      Object.defineProperty(image, 'complete', { get: () => complete[index] })
      element.append(image)
    })
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(element)
    const measure = vi.fn(() => true)
    const scheduler = manualScheduler()
    const measurer = createRowMeasurer({ measure, mountedIds: new Set(), scheduleMicrotask: scheduler.scheduleMicrotask, createObserver: () => undefined })
    try {
      measurer.attachRow('row', element)
      element.dispatchEvent(new Event('load'))
      expect(scheduler.calls).toBe(0)
      complete[0] = true
      images[0]!.dispatchEvent(new Event('load'))
      scheduler.run()
      expect(measure).not.toHaveBeenCalled()
      complete[1] = true
      images[1]!.dispatchEvent(new Event('error'))
      scheduler.run()
      expect(measure).toHaveBeenCalledExactlyOnceWith('row', 0)
    }
    finally {
      measurer.dispose()
      element.remove()
    }
  })

  it('observes an image that mounts after attach and drops its queued retry on detach', () => {
    const element = document.createElement('div')
    let height = 20
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, height)
    document.body.append(element)
    const measure = vi.fn(() => true)
    const scheduler = manualScheduler()
    const measurer = createRowMeasurer({ measure, mountedIds: new Set(), scheduleMicrotask: scheduler.scheduleMicrotask, createObserver: () => undefined })
    try {
      measurer.attachRow('row', element)
      const image = document.createElement('img')
      element.append(image)
      height = 0
      image.dispatchEvent(new Event('error'))
      expect(scheduler.calls).toBe(1)
      measurer.detachRow(element)
      measure.mockClear()
      scheduler.run()
      expect(measure).not.toHaveBeenCalled()
    }
    finally {
      measurer.dispose()
      element.remove()
    }
  })

  it('remeasures the same element on attach even when its previous height was unchanged', () => {
    const measure = vi.fn(() => true)
    const measurer = createRowMeasurer({ measure, mountedIds: new Set(), createObserver: () => undefined })
    const element = fakeEl(40)
    try {
      measurer.attachRow('row', element)
      measurer.detachRow(element)
      measure.mockClear()
      measurer.attachRow('row', element)
      expect(measure).toHaveBeenCalledWith('row', 40)
    }
    finally {
      measurer.dispose()
    }
  })

  it.each(['detach', 'remount', 'dispose'])('removes image retries after %s and keeps a replacement row independent', (operation) => {
    const element = document.createElement('div')
    const image = document.createElement('img')
    let complete = false
    let height = 0
    Object.defineProperty(image, 'complete', { get: () => complete })
    element.append(image)
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, height)
    document.body.append(element)
    const replacement = document.createElement('div')
    const replacementImage = document.createElement('img')
    replacement.append(replacementImage)
    replacement.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(replacement)
    const removeListener = vi.spyOn(element, 'removeEventListener')
    const measure = vi.fn(() => true)
    const scheduler = manualScheduler()
    const mountedIds = new Set<string>()
    const measurer = createRowMeasurer({ measure, mountedIds, scheduleMicrotask: scheduler.scheduleMicrotask, createObserver: () => undefined })
    try {
      measurer.attachRow('row', element)
      complete = true
      image.dispatchEvent(new Event('load'))
      scheduler.run()
      expect(measure).toHaveBeenCalledWith('row', 0)
      if (operation === 'dispose') {
        measurer.dispose()
      }
      else {
        if (operation === 'remount')
          measurer.attachRow('row', replacement)
        measurer.detachRow(element)
      }
      measure.mockClear()
      height = 20
      image.dispatchEvent(new Event('error'))
      scheduler.run()
      expect(measure).not.toHaveBeenCalled()
      expect(removeListener).toHaveBeenCalledWith('load', expect.any(Function), true)
      expect(removeListener).toHaveBeenCalledWith('error', expect.any(Function), true)
      if (operation === 'remount') {
        expect(mountedIds.has('row')).toBe(true)
        replacement.getBoundingClientRect = () => new DOMRect(0, 0, 400, 20)
        replacementImage.dispatchEvent(new Event('load'))
        scheduler.run()
        expect(measure).toHaveBeenCalledWith('row', 20)
      }
    }
    finally {
      measurer.dispose()
      element.remove()
      replacement.remove()
    }
  })

  it('forwards zero on attach and preserves growth smaller than the jitter threshold', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const scheduler = manualScheduler()
    let height = 0
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, 400, height),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const measurer = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: scheduler.scheduleMicrotask,
    })
    measurer.attachRow('empty', element)
    expect(measure).toHaveBeenLastCalledWith('empty', 0)
    measure.mockClear()
    obs.fire([element])
    scheduler.run()
    expect(measure).not.toHaveBeenCalled()
    height = 0.2
    obs.fire([element])
    scheduler.run()
    expect(measure).toHaveBeenLastCalledWith('empty', 0.2)
    height = 0
    obs.fire([element])
    scheduler.run()
    expect(measure).toHaveBeenLastCalledWith('empty', 0)
    measurer.dispose()
  })

  it('keeps an unready zero out of the cache and retries after layout appears', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const scheduler = manualScheduler()
    let width = 0
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, width, 0),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const measurer = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      createObserver: obs.createObserver,
      scheduleMicrotask: scheduler.scheduleMicrotask,
    })
    measurer.attachRow('empty', element)
    expect(measure).not.toHaveBeenCalled()
    expect(obs.observed.has(element)).toBe(true)
    width = 400
    obs.fire([element])
    scheduler.run()
    expect(measure).toHaveBeenCalledWith('empty', 0)
    measurer.dispose()
  })

  it('does not measure a detached row on attach', () => {
    const measure = vi.fn(() => true)
    const obs = fakeObserver()
    const measurer = createRowMeasurer({ measure, mountedIds: new Set(), createObserver: obs.createObserver })
    const element = fakeEl(40, false)
    measurer.attachRow('detached', element)
    expect(measure).not.toHaveBeenCalled()
    expect(obs.observed.has(element)).toBe(true)
    measurer.dispose()
  })

  it('commits the latest zero after visible measurement deferral ends', () => {
    let deferred = true
    let height = 40
    const measure = vi.fn(() => true)
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, 400, height),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const measurer = createRowMeasurer({
      measure,
      mountedIds: new Set(),
      shouldDeferMeasurement: () => deferred,
      createObserver: () => undefined,
    })
    measurer.attachRow('empty', element)
    height = 0
    deferred = false
    expect(measurer.flushDeferredMeasurements()).toBe(true)
    expect(measure).toHaveBeenCalledWith('empty', 0)
    measurer.dispose()
  })

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
      ...fakeRowEvents(),
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
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => ({ height: aHeight }),
    } as unknown as HTMLElement
    const b = {
      ...fakeRowEvents(),
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
      ...fakeRowEvents(),
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
      ...fakeRowEvents(),
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
      ...fakeRowEvents(),
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
