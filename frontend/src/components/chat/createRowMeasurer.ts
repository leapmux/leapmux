import { batch } from 'solid-js'

// ---------------------------------------------------------------------------
// Row measurer: the ResizeObserver / flush DOM glue for the virtualizer
//
// Owns a single ResizeObserver over all mounted rows, the pending-measure set, and
// the batched microtask flush. Extracted from useChatVirtualizer so the offset
// engine isn't interleaved with DOM/microtask machinery, and so the flush TIMING --
// the load-bearing "microtask, not rAF, so a row that grows post-mount re-pins in
// the SAME frame" invariant -- is unit-testable: the scheduler and the observer are
// injectable, so a test drives resize callbacks deterministically with fakes (jsdom
// ships no ResizeObserver). Its only coupling to the rest of the virtualizer is the
// injected `measure` commit and the shared `mountedIds` set.
// ---------------------------------------------------------------------------

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
  /** Commit a single row's measured height (the virtualizer's cache/mean update). */
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
    const height = (el as HTMLElement).getBoundingClientRect().height
    if (stats)
      stats.reads++
    const key = deps.currentMeasurementKey?.(id)
    const last = lastMeasurementByEl.get(el)
    if (last !== undefined && last.id === id && last.key === key && Math.abs(height - last.height) < measureEpsilonPx) {
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
        if (Number.isFinite(height) && height > 0)
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
    // transforms, and the scroll re-pin recompute once. `measure` ignores zero/noise
    // reads and bumps the reactive geomVersion, which drives the consumer's re-pin.
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

  const attachRow = (id: string, el: HTMLElement) => {
    elToId.set(el, id)
    idToEl.set(id, el)
    deps.mountedIds.add(id)
    // Measure immediately so a freshly-mounted row contributes its real height without
    // waiting for the first ResizeObserver tick. `measure` ignores a zero read and
    // bumps the reactive geomVersion, which drives the re-pin.
    const height = el.getBoundingClientRect().height
    if (shouldDeferMeasurement())
      deferred.add(el)
    else
      commitReads([{ id, el, height, key: deps.currentMeasurementKey?.(id) }])
    ro?.observe(el)
  }

  const detachRow = (el: HTMLElement) => {
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
    ro?.disconnect()
    ro = undefined
    idToEl.clear()
    pending.clear()
    deferred.clear()
    // Reset the scheduled flag so the unit is cleanly re-armable rather than wedged
    // (a flush scheduled before dispose can never re-arm scheduleFlush otherwise).
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
