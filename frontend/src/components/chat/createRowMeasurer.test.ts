import type { ResizeObserverLike, RowMeasurer, RowMeasurerDeps } from './createRowMeasurer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createRowHeightCache } from './chatRowHeightCache'
import { createRowMeasurer, MEASURE_EPSILON_PX } from './createRowMeasurer'
import { createRenderReadinessScope } from './renderReadiness'

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
  return {
    createObserver,
    observed,
    fire: (targets: Element[]) => onResize!(targets),
  }
}

/**
 * Attach a row copy with a readiness scope of its own, inside a reactive owner as
 * attachRow requires. The copy holds one body, whose pending state the test sets.
 */
function attachWithReadiness(m: RowMeasurer, id: string, el: HTMLElement, pending: boolean) {
  return createRoot((dispose) => {
    const [bodyPending, setBodyPending] = createSignal(pending)
    const readiness = createRenderReadinessScope()
    readiness.track(bodyPending)
    m.attachRow(id, el, readiness)
    return { setBodyPending, dispose }
  })
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

/**
 * A measurer over a `measure` spy that takes every height, a fake observer, and a manual
 * scheduler. The harness stands in for the height cache: `committedHeight` answers the
 * last height that `measure` took for the row, and `mounted` holds the rows that the
 * mount notices of the measurer report. `deps` overrides any dep, and
 * `createObserver: () => undefined` gives no observer.
 */
function measurerHarness(deps: Partial<RowMeasurerDeps> = {}) {
  const committed = new Map<string, number>()
  const measure = vi.fn((id: string, height: number) => {
    committed.set(id, height)
    return true
  })
  const mounted = new Set<string>()
  const obs = fakeObserver()
  const sched = manualScheduler()
  const m = createRowMeasurer({
    measure,
    committedHeight: id => committed.get(id),
    onRowMounted: id => mounted.add(id),
    onRowUnmounted: id => mounted.delete(id),
    createObserver: obs.createObserver,
    scheduleMicrotask: sched.scheduleMicrotask,
    ...deps,
  })
  return { m, measure, mounted, obs, sched }
}

describe('createRowMeasurer', () => {
  it.each(['load', 'error'])('retries an empty row on image %s without a resize event', (eventName) => {
    const element = document.createElement('div')
    const image = document.createElement('img')
    let complete = false
    Object.defineProperty(image, 'complete', {
      get: () => complete,
    })
    element.append(image)
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(element)
    const { m: measurer, measure, sched: scheduler } = measurerHarness({
      createObserver: () => undefined,
    })
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
    const images = [
      document.createElement('img'),
      document.createElement('img'),
    ]
    const complete = [false, false]
    images.forEach((image, index) => {
      Object.defineProperty(image, 'complete', {
        get: () => complete[index],
      })
      element.append(image)
    })
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(element)
    const { m: measurer, measure, sched: scheduler } = measurerHarness({
      createObserver: () => undefined,
    })
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
    const { m: measurer, measure, sched: scheduler } = measurerHarness({
      createObserver: () => undefined,
    })
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
    const { m: measurer, measure } = measurerHarness({
      createObserver: () => undefined,
    })
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
    Object.defineProperty(image, 'complete', {
      get: () => complete,
    })
    element.append(image)
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, height)
    document.body.append(element)
    const replacement = document.createElement('div')
    const replacementImage = document.createElement('img')
    replacement.append(replacementImage)
    replacement.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    document.body.append(replacement)
    const removeListener = vi.spyOn(element, 'removeEventListener')
    const { m: measurer, measure, mounted, sched: scheduler } = measurerHarness({
      createObserver: () => undefined,
    })
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
        expect(mounted.has('row')).toBe(true)
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

  it('forwards zero on attach, and drops a change below the jitter threshold from the committed height, also from and to zero', () => {
    let height = 0
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, 400, height),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const { m: measurer, measure, obs, sched: scheduler } = measurerHarness()
    const resizeTo = (next: number): void => {
      height = next
      obs.fire([element])
      scheduler.run()
    }
    measurer.attachRow('empty', element)
    expect(measure).toHaveBeenLastCalledWith('empty', 0)
    measure.mockClear()

    // The height cache keeps a height within the threshold of a new read, whether or not
    // one of the two is zero. So the measurer drops the same reads.
    resizeTo(0)
    resizeTo(0.2)
    expect(measure).not.toHaveBeenCalled()

    resizeTo(0.6)
    expect(measure).toHaveBeenLastCalledWith('empty', 0.6)
    measure.mockClear()

    resizeTo(0.3)
    expect(measure).not.toHaveBeenCalled()

    resizeTo(0)
    expect(measure).toHaveBeenLastCalledWith('empty', 0)
    measurer.dispose()
  })

  it.each([
    { cached: 0, firstRead: 0.3, secondRead: 0.7 },
    { cached: 100, firstRead: 100.4, secondRead: 100.8 },
  ])('commits a read that differs from the cached $cached by the jitter threshold after the cache kept its height at the read before', ({ cached, firstRead, secondRead }) => {
    // The measurer and the cache must compare with the same height. The cache keeps its
    // height at the first read, which lies inside the threshold. The second read lies
    // outside the threshold of the cached height, so the cache must take it.
    const row = { id: 'row', hasSpanLines: false, heightKey: 'k' }
    const cache = createRowHeightCache({
      loadedIds: () => [],
    })
    cache.commitAll([
      { row, height: cached },
    ])
    let height = firstRead
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, 400, height),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const { m: measurer, obs, sched: scheduler } = measurerHarness({
      measure: (_id, read) => cache.commit(row, read, 'k', 'visible') === 'moved',
      committedHeight: () => cache.heightUnderCurrentKey(row),
      onRowMounted: cache.noteMounted,
      onRowUnmounted: id => cache.noteDetached(id),
      currentMeasurementKey: () => 'k',
    })

    measurer.attachRow('row', element)
    expect(cache.heightOf(row)).toBe(cached)
    height = secondRead
    obs.fire([element])
    scheduler.run()

    expect(cache.heightOf(row)).toBe(secondRead)
    measurer.dispose()
  })

  it('keeps an unready zero out of the cache and retries after layout appears', () => {
    let width = 0
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, width, 0),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const { m: measurer, measure, obs, sched: scheduler } = measurerHarness()
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
    const { m: measurer, measure, obs } = measurerHarness()
    const element = fakeEl(40, false)
    measurer.attachRow('detached', element)
    expect(measure).not.toHaveBeenCalled()
    expect(obs.observed.has(element)).toBe(true)
    measurer.dispose()
  })

  it('reads no height from a row copy that waits for content, on attach or on resize', () => {
    const onFlush = vi.fn()
    const { m, measure, obs, sched: scheduler } = measurerHarness({ onFlush })
    const el = fakeEl(40)

    const { dispose } = attachWithReadiness(m, 'r1', el, true)
    obs.fire([el])
    scheduler.run()

    expect(measure).not.toHaveBeenCalled()
    expect(onFlush).toHaveBeenCalledWith({
      targets: 1,
      reads: 0,
      committed: 0,
      skippedDetached: 0,
      skippedNotReady: 1,
      skippedUnchanged: 0,
    })
    dispose()
  })

  it('measures a row copy again once it turns ready, with no resize', () => {
    const { m, measure, sched: scheduler } = measurerHarness()
    const el = fakeEl(120)
    const { setBodyPending, dispose } = attachWithReadiness(m, 'r1', el, true)
    expect(measure).not.toHaveBeenCalled()
    expect(scheduler.calls).toBe(0)

    setBodyPending(false)
    scheduler.run()

    expect(measure).toHaveBeenCalledOnce()
    expect(measure).toHaveBeenCalledWith('r1', 120)
    dispose()
  })

  it('measures a row copy that is ready from the start on attach, and never again without a resize', () => {
    const { m, measure, sched: scheduler } = measurerHarness()
    const el = fakeEl(120)

    const { dispose } = attachWithReadiness(m, 'r1', el, false)

    expect(measure).toHaveBeenCalledOnce()
    expect(measure).toHaveBeenCalledWith('r1', 120)
    expect(scheduler.calls).toBe(0)
    dispose()
  })

  it('reads nothing from a row copy after detachRow, even when it turns ready', () => {
    const { m, measure, sched: scheduler } = measurerHarness()
    const el = fakeEl(120)
    const { setBodyPending, dispose } = attachWithReadiness(m, 'r1', el, true)

    m.detachRow(el)
    setBodyPending(false)
    scheduler.run()

    expect(scheduler.calls).toBe(0)
    expect(measure).not.toHaveBeenCalled()
    dispose()
  })

  it('reads nothing from a row copy that left the document when it turns ready', () => {
    const { m, measure, sched: scheduler } = measurerHarness()
    const el = fakeEl(120)
    const { setBodyPending, dispose } = attachWithReadiness(m, 'r1', el, true)

    el.isConnected = false
    setBodyPending(false)
    scheduler.run()

    expect(scheduler.calls).toBe(0)
    expect(measure).not.toHaveBeenCalled()
    dispose()
  })

  it('stops following the readiness of a row copy whose owner is disposed', () => {
    const { m, measure, sched: scheduler } = measurerHarness()
    const el = fakeEl(120)
    const { setBodyPending, dispose } = attachWithReadiness(m, 'r1', el, true)

    dispose()
    setBodyPending(false)

    expect(scheduler.calls).toBe(0)
    expect(measure).not.toHaveBeenCalled()
  })

  // The readiness is keyed by element, and an element can attach again with no detach
  // between. A readiness that the new attach does not pass must not hold it.
  it('reads an element that attaches again without readiness, whatever its earlier readiness said', () => {
    const { m, measure } = measurerHarness()
    const el = fakeEl(120)
    const { dispose } = attachWithReadiness(m, 'r1', el, true)
    expect(measure).not.toHaveBeenCalled()

    m.attachRow('r1', el)

    expect(measure).toHaveBeenCalledOnce()
    expect(measure).toHaveBeenCalledWith('r1', 120)
    dispose()
  })

  // Under an attach-before-detach remount, two copies of one id coexist for a moment,
  // and each waits for its own content.
  describe('two copies of one id', () => {
    it('measures a ready new copy while the old copy still waits', () => {
      const { m, measure } = measurerHarness()
      const oldCopy = attachWithReadiness(m, 'r1', fakeEl(120), true)

      const newCopy = attachWithReadiness(m, 'r1', fakeEl(160), false)

      expect(measure).toHaveBeenCalledOnce()
      expect(measure).toHaveBeenCalledWith('r1', 160)
      oldCopy.dispose()
      newCopy.dispose()
    })

    it('reads nothing from a waiting new copy, and ignores the old copy turning ready', () => {
      const { m, measure, obs, sched: scheduler } = measurerHarness()
      const oldEl = fakeEl(120)
      const newEl = fakeEl(160)
      const oldCopy = attachWithReadiness(m, 'r1', oldEl, true)
      const newCopy = attachWithReadiness(m, 'r1', newEl, true)

      // The old copy's content lands after the new copy replaced it: it owns no
      // measurement of the id any more, so it queues nothing.
      oldCopy.setBodyPending(false)
      expect(scheduler.calls).toBe(0)
      obs.fire([newEl])
      scheduler.run()
      expect(measure).not.toHaveBeenCalled()

      newCopy.setBodyPending(false)
      scheduler.run()
      expect(measure).toHaveBeenCalledOnce()
      expect(measure).toHaveBeenCalledWith('r1', 160)
      oldCopy.dispose()
      newCopy.dispose()
    })
  })

  it('measures and observes a freshly-mounted row, and reports it mounted', () => {
    const { m, measure, mounted, obs } = measurerHarness()
    const el = fakeEl(120)
    m.attachRow('r1', el)
    expect(measure).toHaveBeenCalledWith('r1', 120)
    expect(mounted.has('r1')).toBe(true)
    expect(obs.observed.has(el)).toBe(true)
  })

  it('coalesces resize ticks into ONE scheduled flush that commits each measurement', () => {
    const { m, measure, obs, sched } = measurerHarness()
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
    const onFlush = vi.fn()
    const { m, measure, obs, sched } = measurerHarness({ onFlush })
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
      skippedNotReady: 0,
      skippedUnchanged: 1,
    })
  })

  it('measures a reused element when it is reattached under a different row id', () => {
    const { m, measure } = measurerHarness({
      currentMeasurementKey: () => 'same-key',
    })
    const el = fakeEl(100)

    m.attachRow('old-row', el)
    measure.mockClear()
    m.attachRow('new-row', el)

    // The same height under the same key, but another row: the attach reads it again.
    expect(measure).toHaveBeenCalledOnce()
    expect(measure).toHaveBeenCalledWith('new-row', 100)
  })

  it('commits a zero height at once', () => {
    const element = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => new DOMRect(0, 0, 400, 0),
      querySelectorAll: () => [],
    } as unknown as HTMLElement
    const { m, measure } = measurerHarness({
      createObserver: () => undefined,
    })

    m.attachRow('empty', element)

    expect(measure).toHaveBeenCalledOnce()
    expect(measure).toHaveBeenCalledWith('empty', 0)
  })

  it('re-commits unchanged DOM heights when the row measurement key changed', () => {
    let key = 'old'
    const { m, measure, obs, sched } = measurerHarness({
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

  describe('measureAgain', () => {
    it('reads a mounted row again at the next flush, with no resize, under its new key', () => {
      let key = 'old'
      const { m, measure, sched } = measurerHarness({
        currentMeasurementKey: () => key,
      })
      m.attachRow('a', fakeEl(100))
      measure.mockClear()

      key = 'new'
      m.measureAgain('a')
      expect(measure).not.toHaveBeenCalled()
      sched.run()

      expect(measure).toHaveBeenCalledOnce()
      expect(measure).toHaveBeenCalledWith('a', 100)
    })

    it('does nothing for a row that is not mounted', () => {
      const { m, measure, sched } = measurerHarness()
      const a = fakeEl(100)
      m.attachRow('a', a)
      m.detachRow(a)
      measure.mockClear()

      m.measureAgain('a')
      m.measureAgain('never-mounted')

      expect(sched.calls).toBe(0)
      expect(measure).not.toHaveBeenCalled()
    })

    it('reads nothing from a row copy that replaces its content, and reads it once the copy shows the new content', () => {
      let key = 'old'
      const { m, measure, sched } = measurerHarness({
        currentMeasurementKey: () => key,
      })
      let height = 100
      const el = {
        ...fakeEl(0),
        getBoundingClientRect: () => ({ height }),
      } as unknown as HTMLElement
      const [replacing, setReplacing] = createSignal(false)
      const dispose = createRoot((disposeRoot) => {
        const readiness = createRenderReadinessScope()
        readiness.trackReplacing(replacing)
        m.attachRow('a', el, readiness)
        return disposeRoot
      })
      expect(measure).toHaveBeenCalledWith('a', 100)
      measure.mockClear()

      setReplacing(true)
      key = 'new'
      m.measureAgain('a')
      sched.run()
      expect(measure).not.toHaveBeenCalled()

      height = 60
      setReplacing(false)
      sched.run()

      expect(measure).toHaveBeenCalledOnce()
      expect(measure).toHaveBeenCalledWith('a', 60)
      dispose()
    })
  })

  it('commits ResizeObserver measurements that move beyond epsilon', () => {
    let height = 100
    const { m, measure, obs, sched } = measurerHarness()
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

  it('skips a resize below MEASURE_EPSILON_PX and commits one of MEASURE_EPSILON_PX', () => {
    let height = 100
    const { m, measure, obs, sched } = measurerHarness()
    const a = {
      ...fakeRowEvents(),
      isConnected: true,
      getBoundingClientRect: () => ({ height }),
    } as unknown as HTMLElement
    m.attachRow('a', a)
    measure.mockClear()

    height = 100 + MEASURE_EPSILON_PX - 0.01
    obs.fire([a])
    sched.run()
    expect(measure).not.toHaveBeenCalled()
    expect(m.hasUncommittedHeight('a')).toBe(false)

    // A skipped read leaves the last accepted read at 100, so the distance is the full epsilon.
    height = 100 + MEASURE_EPSILON_PX
    expect(m.hasUncommittedHeight('a')).toBe(true)
    obs.fire([a])
    sched.run()
    expect(measure).toHaveBeenCalledExactlyOnceWith('a', 100 + MEASURE_EPSILON_PX)
  })

  it('skips a disconnected element during the flush', () => {
    const { m, measure, obs, sched } = measurerHarness()
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

  it('unobserves, reports the unmount, and drops a pending measurement when detachRow runs', () => {
    const { m, measure, mounted, obs, sched } = measurerHarness()
    const a = fakeEl(100)
    m.attachRow('a', a)
    obs.fire([a]) // a is now pending a measurement
    measure.mockClear()

    m.detachRow(a)
    expect(mounted.has('a')).toBe(false)
    expect(obs.observed.has(a)).toBe(false)

    sched.run() // the pending flush must not measure the detached row
    expect(measure).not.toHaveBeenCalled()
  })

  it('drops the el->id mapping in detachRow so a late resize for it does not measure', () => {
    const { m, measure, obs, sched } = measurerHarness()
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
    // detaching the OLD element must not report the unmount of the freshly-mounted row.
    const { m, mounted, obs } = measurerHarness()
    const oldEl = fakeEl(100)
    const newEl = fakeEl(120)
    m.attachRow('r1', oldEl)
    m.attachRow('r1', newEl) // new element claims r1 before oldEl's cleanup
    m.detachRow(oldEl) // oldEl's deferred cleanup -- must NOT unmount r1
    expect(mounted.has('r1')).toBe(true)
    expect(obs.observed.has(newEl)).toBe(true)
    // The newer element's own detach finally relinquishes the id.
    m.detachRow(newEl)
    expect(mounted.has('r1')).toBe(false)
  })

  it('reports each attach of a row, and its unmount once, when its last element detaches', () => {
    const onRowMounted = vi.fn()
    const onRowUnmounted = vi.fn()
    const { m } = measurerHarness({ onRowMounted, onRowUnmounted })
    const oldEl = fakeEl(100)
    const newEl = fakeEl(120)
    m.attachRow('r1', oldEl)
    m.attachRow('r1', newEl)
    expect(onRowMounted.mock.calls).toEqual([
      ['r1'],
      ['r1'],
    ])

    m.detachRow(oldEl)
    expect(onRowUnmounted).not.toHaveBeenCalled()

    m.detachRow(newEl)
    expect(onRowUnmounted.mock.calls).toEqual([
      ['r1'],
    ])
    // A second detach of the same element unmounts nothing more.
    m.detachRow(newEl)
    expect(onRowUnmounted).toHaveBeenCalledTimes(1)
  })

  it('skips a pending resize from an old connected element after a new element claims the same id', () => {
    const { m, measure, obs, sched } = measurerHarness()
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
    const { m, obs } = measurerHarness()
    m.attachRow('a', fakeEl(100))
    expect(obs.observed.size).toBe(1)
    m.dispose()
    expect(obs.observed.size).toBe(0)
  })

  it('dispose resets the scheduled flag so a later tick can re-arm a flush', () => {
    const { m, obs, sched } = measurerHarness()
    const a = fakeEl(100)
    m.attachRow('a', a)
    obs.fire([a]) // schedules a flush (flushScheduled = true)
    expect(sched.calls).toBe(1)
    m.dispose() // must clear flushScheduled, else scheduleFlush can never re-arm
    obs.fire([a])
    expect(sched.calls).toBe(2)
  })

  describe('elementOf and hasUncommittedHeight', () => {
    /** A measurer with one row `a` attached, whose rectangle the test resizes. */
    function attachedRow() {
      const { m, measure, obs, sched } = measurerHarness()
      const row = { height: 100, connected: true }
      const el = {
        ...fakeRowEvents(),
        get isConnected() {
          return row.connected
        },
        getBoundingClientRect: () => ({ height: row.height }),
      } as unknown as HTMLElement
      m.attachRow('a', el)
      return { m, el, row, measure, obs, sched }
    }

    it('returns the attached element of a row', () => {
      const { m, el } = attachedRow()
      expect(m.elementOf('a')).toBe(el)
    })

    it('returns undefined after the detach and for an unknown id', () => {
      const { m, el } = attachedRow()
      m.detachRow(el)
      expect(m.elementOf('a')).toBeUndefined()
      expect(m.elementOf('never-attached')).toBeUndefined()
    })

    it('reports no uncommitted height after a commit', () => {
      const { m, measure } = attachedRow()
      expect(m.hasUncommittedHeight('a')).toBe(false)
      expect(measure).toHaveBeenCalledOnce()
      expect(measure).toHaveBeenCalledWith('a', 100)
    })

    it('reports an uncommitted height when the element changed', () => {
      const { m, row, measure } = attachedRow()
      row.height = 130
      expect(m.hasUncommittedHeight('a')).toBe(true)
      // The read commits nothing and records nothing: it reports the same answer again.
      expect(m.hasUncommittedHeight('a')).toBe(true)
      expect(measure).toHaveBeenCalledOnce()
    })

    it('reports none for a change below the jitter threshold', () => {
      const { m, row } = attachedRow()
      row.height = 100.3
      expect(m.hasUncommittedHeight('a')).toBe(false)
    })

    it('reports none after the flush committed the change', () => {
      const { m, el, row, measure, obs, sched } = attachedRow()
      row.height = 130
      obs.fire([el])
      sched.run()
      expect(measure).toHaveBeenLastCalledWith('a', 130)
      expect(m.hasUncommittedHeight('a')).toBe(false)
    })

    it('reports none for an unknown id and for a detached element', () => {
      const { m, row } = attachedRow()
      expect(m.hasUncommittedHeight('never-attached')).toBe(false)
      row.height = 130
      row.connected = false
      expect(m.hasUncommittedHeight('a')).toBe(false)
    })

    it('reports none for a row copy that waits for content', () => {
      const { m } = measurerHarness()
      const { dispose } = attachWithReadiness(m, 'a', fakeEl(100), true)
      expect(m.hasUncommittedHeight('a')).toBe(false)
      dispose()
    })
  })

  it('still measures immediately when no observer is available (non-DOM env)', () => {
    const { m, measure } = measurerHarness({
      createObserver: () => undefined,
    })
    m.attachRow('a', fakeEl(140))
    expect(measure).toHaveBeenCalledWith('a', 140)
  })
})
