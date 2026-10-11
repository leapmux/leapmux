import { batch } from 'solid-js'
import { rowLayoutMeasurement } from './rowLayoutMeasurement'

// Row measurement for the virtualizer.
//
// This module owns the ResizeObserver, image event handlers, and measurement queue.
// It keeps DOM reads and scheduling separate from the offset engine.
// The virtualizer owns the height cache and the shared mountedIds set.
//
// A microtask runs after a resize handler and before paint. A row that grows
// therefore updates its offsets and scroll anchor in the same frame.
// A requestAnimationFrame handler would paint stale sibling offsets first.
// Native momentum defers only the commits while the browser owns scrollTop.
// Tests inject the observer and scheduler to control these transitions.

/** The subset of ResizeObserver the measurer uses (so a test can inject a fake). */
export interface ResizeObserverLike {
  observe: (el: Element) => void
  unobserve: (el: Element) => void
  disconnect: () => void
}

export interface RowMeasurer {
  /** Observe a freshly-mounted row and take its immediate measurement. */
  attachRow: (id: string, el: HTMLElement) => void
  /** Stop observing an unmounting row (its cached height is kept for a flash-free return). */
  detachRow: (el: HTMLElement) => void
  /** Whether at least one visible-row measurement is queued behind the deferral gate. */
  hasDeferredMeasurements: () => boolean
  /** Commit any measurements queued while visible measurement commits were deferred. */
  flushDeferredMeasurements: () => boolean
  /** Flush any pending measurements synchronously (teardown / tests). */
  flushNow: () => void
  /** Disconnect the observer and drop the pending set (on cleanup). */
  dispose: () => void
}

export interface RowMeasurerDeps {
  /** Commit one row height to the virtualizer's cache and estimate. */
  measure: (id: string, height: number) => boolean
  /**
   * Current measurement key for a mounted row. Used only for the DOM-read de-dupe:
   * equal heights under different keys still need to reach the virtualizer so it can
   * refresh the keyed height cache.
   */
  currentMeasurementKey?: (id: string) => string | undefined
  /**
   * The shared mounted-row set the measurer adds to on attach and removes from on
   * detach. Read elsewhere (the height-cache eviction protect set, the UI-state cap),
   * so it is owned by the virtualizer and threaded in rather than owned here.
   */
  mountedIds: Set<string>
  /**
   * Schedule `flush` to run before paint. Default: queueMicrotask -- a microtask
   * queued from a ResizeObserver callback runs after the callback but BEFORE paint, so
   * a row that grows post-mount (async syntax highlighting, expand/collapse) normally
   * updates the offset map and re-pins scrollTop in the SAME frame it grew. During a
   * native momentum fling the deferral gate below queues the commit instead, because
   * re-pin intentionally avoids scrollTop writes while the browser owns momentum.
   * Deferring the scheduler itself to rAF would still paint stale sibling offsets in
   * non-fling cases -- a visible vertical wiggle while scrolling. Injected for tests.
   */
  scheduleMicrotask?: (cb: () => void) => void
  /**
   * Build the ResizeObserver from a targets callback. Default: the global
   * ResizeObserver (undefined in a non-DOM env, where attach still measures
   * immediately). Injected for tests with a fake whose callback the test can fire.
   */
  createObserver?: (onResize: (targets: Element[]) => void) => ResizeObserverLike | undefined
  /**
   * ResizeObserver callbacks can fire for sub-pixel font/layout jitter while the
   * user scrolls. Reads within this epsilon of the last accepted DOM read are
   * dropped before they call into the virtualizer, avoiding no-op reactive commits
   * and repeated offset-map work. Defaults to half a CSS pixel.
   */
  measureEpsilonPx?: number
  /**
   * Gate visible-row height commits while a native momentum fling owns scrollTop.
   * Reads still happen and rows still mount/observe; committing is delayed so the
   * virtual spacer/translateY map does not churn by one-line estimate corrections
   * while re-pin is deliberately not writing scrollTop.
   */
  shouldDeferMeasurement?: () => boolean
  /** Optional test/perf hook for ResizeObserver churn accounting. */
  onFlush?: (stats: RowMeasureFlushStats) => void
}

function defaultCreateObserver(onResize: (targets: Element[]) => void): ResizeObserverLike | undefined {
  if (typeof ResizeObserver === 'undefined')
    return undefined
  return new ResizeObserver(entries => onResize(entries.map(e => e.target)))
}

export interface RowMeasureFlushStats {
  targets: number
  reads: number
  committed: number
  skippedDetached: number
  skippedUnchanged: number
}

const DEFAULT_MEASURE_EPSILON_PX = 0.5

interface MeasurementRead {
  id: string
  el: Element
  height: number
  key: string | undefined
}

export function createRowMeasurer(deps: RowMeasurerDeps): RowMeasurer {
  const elToId = new WeakMap<Element, string>()
  const lastMeasurementByEl = new WeakMap<Element, { id: string, height: number, key: string | undefined }>()
  // The element that currently OWNS each mounted id. Under an attach-before-detach
  // remount (a row remounts under a new element before the old element's cleanup runs),
  // two elements transiently map to the same id; detachRow must relinquish the id's
  // mounted-protection only for the element that still owns it, or it would un-protect
  // the freshly-mounted row from height-cache eviction.
  const idToEl = new Map<string, Element>()
  const pending = new Set<Element>()
  const deferred = new Set<Element>()
  const imageListeners = new Map<HTMLElement, EventListener>()
  let flushScheduled = false
  let ro: ResizeObserverLike | undefined
  const schedule = deps.scheduleMicrotask ?? queueMicrotask
  const measureEpsilonPx = Math.max(0, deps.measureEpsilonPx ?? DEFAULT_MEASURE_EPSILON_PX)

  const shouldDeferMeasurement = () => deps.shouldDeferMeasurement?.() ?? false

  const readMeasurement = (
    el: Element,
    stats?: Pick<RowMeasureFlushStats, 'reads' | 'skippedDetached' | 'skippedUnchanged'>,
  ): MeasurementRead | undefined => {
    const id = elToId.get(el)
    if (id === undefined || !el.isConnected || idToEl.get(id) !== el) {
      if (stats)
        stats.skippedDetached++
      return undefined
    }
    const height = rowLayoutMeasurement(el)
    if (stats)
      stats.reads++
    if (height === undefined)
      return undefined
    const key = deps.currentMeasurementKey?.(id)
    const last = lastMeasurementByEl.get(el)
    if (last !== undefined && last.id === id && last.key === key && (height === 0) === (last.height === 0) && Math.abs(height - last.height) < measureEpsilonPx) {
      if (stats)
        stats.skippedUnchanged++
      return undefined
    }
    return { id, el, height, key }
  }

  const commitReads = (reads: MeasurementRead[]): number => {
    let committed = 0
    batch(() => {
      for (const { id, el, height, key } of reads) {
        if (key !== deps.currentMeasurementKey?.(id))
          continue
        if (deps.measure(id, height))
          committed++
        lastMeasurementByEl.set(el, { id, height, key })
      }
    })
    return committed
  }

  const flush = () => {
    flushScheduled = false
    // Read all heights first (batched), then commit -- avoids interleaved read/write
    // layout thrash.
    const reads: MeasurementRead[] = []
    const stats: RowMeasureFlushStats = {
      targets: pending.size,
      reads: 0,
      committed: 0,
      skippedDetached: 0,
      skippedUnchanged: 0,
    }
    for (const el of pending) {
      const read = readMeasurement(el, stats)
      if (read)
        reads.push(read)
    }
    pending.clear()
    if (shouldDeferMeasurement()) {
      for (const { el } of reads)
        deferred.add(el)
      deps.onFlush?.(stats)
      return
    }
    // Commit all measurements in one reactive batch so the offset map, the row
    // transforms, and the scroll position update once. The layout check rejects
    // unready reads before they reach the height cache.
    stats.committed = commitReads(reads)
    deps.onFlush?.(stats)
  }

  const flushDeferredMeasurements = (): boolean => {
    if (deferred.size === 0)
      return false
    const reads: MeasurementRead[] = []
    for (const el of deferred) {
      const read = readMeasurement(el)
      if (read)
        reads.push(read)
    }
    deferred.clear()
    return commitReads(reads) > 0
  }

  const scheduleFlush = () => {
    if (flushScheduled)
      return
    flushScheduled = true
    schedule(flush)
  }

  ro = (deps.createObserver ?? defaultCreateObserver)((targets) => {
    for (const t of targets)
      pending.add(t)
    scheduleFlush()
  })

  const observeImages = (el: HTMLElement) => {
    if (imageListeners.has(el))
      return
    const onImageSettled: EventListener = (event) => {
      if (!(event.target instanceof Element) || event.target.localName !== 'img')
        return
      const id = elToId.get(el)
      if (!el.isConnected || id === undefined || idToEl.get(id) !== el)
        return
      pending.add(el)
      scheduleFlush()
    }
    imageListeners.set(el, onImageSettled)
    // Capture load and error because image events do not bubble.
    // The row listener also covers images that mount after attachRow.
    el.addEventListener('load', onImageSettled, true)
    el.addEventListener('error', onImageSettled, true)
  }

  const unobserveImages = (el: HTMLElement) => {
    const handler = imageListeners.get(el)
    if (handler === undefined)
      return
    el.removeEventListener('load', handler, true)
    el.removeEventListener('error', handler, true)
    imageListeners.delete(el)
  }

  const attachRow = (id: string, el: HTMLElement) => {
    elToId.set(el, id)
    idToEl.set(id, el)
    deps.mountedIds.add(id)
    // Each attach takes a fresh read even when the previous element was the same.
    lastMeasurementByEl.delete(el)
    observeImages(el)
    // Measure a mounted row immediately. The observer retries an unready read.
    const read = readMeasurement(el)
    if (shouldDeferMeasurement())
      deferred.add(el)
    else if (read)
      commitReads([read])
    ro?.observe(el)
  }

  const detachRow = (el: HTMLElement) => {
    unobserveImages(el)
    ro?.unobserve(el)
    pending.delete(el)
    deferred.delete(el)
    const id = elToId.get(el)
    // Relinquish the id's mounted-protection only if THIS element still owns it. Under
    // an attach-before-detach remount the id was already re-claimed by a new element, so
    // deleting it here would un-protect the now-mounted row from height-cache eviction
    // (a measure overflowing the cap could then evict its just-measured height, forcing a
    // fallback-height + visible re-pin). The newer element's later detach clears it.
    if (id !== undefined && idToEl.get(id) === el) {
      deps.mountedIds.delete(id)
      idToEl.delete(id)
    }
    // Drop the el->id mapping so detach is a clean inverse of attach: an element
    // later re-attached under a DIFFERENT id can't have a stale entry resolve a
    // pending measurement to the wrong id. The cached HEIGHT (keyed by id in the
    // virtualizer, not here) is untouched, so re-entering the row stays flash-free.
    elToId.delete(el)
  }

  const dispose = () => {
    for (const el of imageListeners.keys())
      unobserveImages(el)
    ro?.disconnect()
    ro = undefined
    idToEl.clear()
    pending.clear()
    deferred.clear()
    // Clear the scheduled flag so a later attach can schedule another flush.
    // Disposal clears the pending reads before the queued flush runs.
    flushScheduled = false
  }

  return {
    attachRow,
    detachRow,
    hasDeferredMeasurements: () => deferred.size > 0,
    flushDeferredMeasurements,
    flushNow: flush,
    dispose,
  }
}
