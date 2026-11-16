import type { RenderReadinessScope } from './renderReadiness'
import { batch } from 'solid-js'
import { rowLayoutMeasurement } from './rowLayoutMeasurement'

// Row measurement for the virtualizer.
//
// This module owns the ResizeObserver, image event handlers, and measurement queue.
// It keeps DOM reads and scheduling separate from the offset engine.
// The height cache (see createRowHeightCache) owns the heights and the set of the mounted
// rows. ChatView creates it, and the virtualizer connects this module to it through the
// deps below.
//
// A microtask runs after a resize handler and before paint. A row that grows
// therefore updates its offsets and the layout shift in the same frame.
// A requestAnimationFrame handler would paint stale sibling offsets first.
// Tests inject the observer and scheduler to control these transitions.

/** The subset of ResizeObserver the measurer uses (so a test can inject a fake). */
export interface ResizeObserverLike {
  observe: (el: Element) => void
  unobserve: (el: Element) => void
  disconnect: () => void
}

/**
 * The readiness of one mounted row copy, as the measurer reads it: whether the
 * height of the copy is final, and the moment it turns final.
 */
export type RowContentReadiness = Pick<RenderReadinessScope, 'measurable' | 'onMeasurable'>

export interface RowMeasurer {
  /**
   * Observe a freshly-mounted row and take its immediate measurement.
   *
   * `readiness` is the readiness of this row copy. While it is not measurable, the
   * copy waits for content (a Markdown body or an edit diff that a worker still
   * computes), or a body mounts new content behind the content that it shows (a
   * diff view that switches between unified and split). Its height is wrong by
   * exactly that content, so the measurer reads nothing from it: committing the
   * height would move every row below twice. When the copy turns measurable, the
   * measurer measures it again at the next flush, whether or not it resized: the
   * content usually resizes it, but nothing guarantees that, and a missed read
   * would leave the row unmeasured. Without `readiness`, the row is always
   * measurable.
   *
   * Call it within the row copy's reactive owner when it passes `readiness`: the
   * subscription to the copy's readiness ends when that owner is disposed.
   */
  attachRow: (id: string, el: HTMLElement, readiness?: RowContentReadiness) => void
  /** Stop observing an unmounting row (its cached height is kept for a flash-free return). */
  detachRow: (el: HTMLElement) => void
  /**
   * Measure the mounted row `id` again at the next flush, whether or not it
   * resized: its height key changed, and the height that it shows may hold under
   * the new key, which only a read can confirm. A row that is not mounted has
   * nothing to read, and a copy that is not measurable waits for its turn.
   */
  measureAgain: (id: string) => void
  /** The element that currently owns the mounted row `id`, or `undefined`. */
  elementOf: (id: string) => Element | undefined
  /**
   * Whether a flush would pass a read of the row `id` to `measure` now: its element is the
   * connected owner of the id, it is measurable, and the read rule does not drop its height
   * (see MEASURE_EPSILON_PX). False for an unknown id. It reads layout and changes no
   * state.
   *
   * An animation frame runs before the ResizeObserver delivery of the same rendering
   * update, so a frame can see a reflow that no flush committed yet. This answers that
   * question with the read rule of the flush itself, so the two cannot disagree.
   */
  hasUncommittedHeight: (id: string) => boolean
  /** Flush any pending measurements synchronously (teardown / tests). */
  flushNow: () => void
  /** Disconnect the observer and drop the pending set (on cleanup). */
  dispose: () => void
}

export interface RowMeasurerDeps {
  /** Commit one row height through the virtualizer to the height cache. */
  measure: (id: string, height: number) => boolean
  /**
   * Current measurement key for a mounted row. Used only for the DOM-read de-dupe:
   * equal heights under different keys still need to reach the virtualizer so it can
   * refresh the keyed height cache.
   */
  currentMeasurementKey?: (id: string) => string | undefined
  /**
   * The height that the height cache holds for the row `id` under its current measurement
   * key, or `undefined`. The read rule compares each read with it (see
   * MEASURE_EPSILON_PX), so the measurer and the cache judge a read against one height.
   */
  committedHeight: (id: string) => number | undefined
  /**
   * Called at each attach of an element for row `id`. The height cache keeps the set of
   * the mounted rows from this notice and the next one (see RowHeightCache.mountedIds).
   */
  onRowMounted: (id: string) => void
  /**
   * Called when the last element of row `id` detaches. A detach of an element that a
   * newer element of the same row replaced does not call it.
   */
  onRowUnmounted: (id: string) => void
  /**
   * Schedule `flush` to run before paint. Default: queueMicrotask -- a microtask
   * queued from a ResizeObserver callback runs after the callback but BEFORE paint, so
   * a row that grows post-mount (async syntax highlighting, expand/collapse) updates
   * the offset map and the layout shift in the SAME frame it grew. Deferring the
   * scheduler to rAF would paint stale sibling offsets -- a visible vertical wiggle
   * while scrolling. Injected for tests.
   */
  scheduleMicrotask?: (cb: () => void) => void
  /**
   * Build the ResizeObserver from a targets callback. Default: the global
   * ResizeObserver (undefined in a non-DOM env, where attach still measures
   * immediately). Injected for tests with a fake whose callback the test can fire.
   */
  createObserver?: (onResize: (targets: Element[]) => void) => ResizeObserverLike | undefined
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
  /** Rows not read because they are not measurable: they wait for content, or replace it (see `RowMeasurer.attachRow`). */
  skippedNotReady: number
  skippedUnchanged: number
}

/**
 * The sub-pixel jitter (px) of a row height that changes no geometry. A ResizeObserver
 * callback can fire for font or layout jitter while the user scrolls. The height cache
 * keeps a cached height within this distance of a new measure (see createRowHeightCache).
 * The measurer drops a read within this distance of that cached height before it calls
 * into the virtualizer, once the element gave a read under the same key. Both compare
 * with the height of the cache, so the DOM read and the offset map ignore the same
 * jitter, and a row that changes in steps below this distance cannot move away from its
 * cached height by more than it.
 */
export const MEASURE_EPSILON_PX = 0.5

interface MeasurementRead {
  id: string
  el: Element
  height: number
  key: string | undefined
}

export function createRowMeasurer(deps: RowMeasurerDeps): RowMeasurer {
  const elToId = new WeakMap<Element, string>()
  // The id and the key of the last read of each element that reached `measure`. The first
  // read of an element under a key always reaches `measure`: it tells the virtualizer that
  // the row shows its height (see UseChatVirtualizerResult.showsHeight), and it lets the
  // cache confirm a height under a new key.
  const lastReadByEl = new WeakMap<Element, { id: string, key: string | undefined }>()
  // The element that currently OWNS each mounted id. Under an attach-before-detach
  // remount (a row remounts under a new element before the old element's cleanup runs),
  // two elements transiently map to the same id; detachRow must report the unmount of
  // the id only for the element that still owns it, or the row that just mounted would
  // count as unmounted.
  const idToEl = new Map<string, Element>()
  // The readiness of each attached row copy that passed one. Keyed by ELEMENT, not by
  // id: under an attach-before-detach remount, two copies of one id coexist, and each
  // copy waits for its own content.
  const readinessByEl = new WeakMap<Element, RowContentReadiness>()
  const pending = new Set<Element>()
  const imageListeners = new Map<HTMLElement, EventListener>()
  let flushScheduled = false
  let ro: ResizeObserverLike | undefined
  const schedule = deps.scheduleMicrotask ?? queueMicrotask

  const readMeasurement = (
    el: Element,
    stats?: Pick<RowMeasureFlushStats, 'reads' | 'skippedDetached' | 'skippedNotReady' | 'skippedUnchanged'>,
  ): MeasurementRead | undefined => {
    const id = elToId.get(el)
    if (id === undefined || !el.isConnected || idToEl.get(id) !== el) {
      if (stats)
        stats.skippedDetached++
      return undefined
    }
    if (readinessByEl.get(el)?.measurable() === false) {
      if (stats)
        stats.skippedNotReady++
      return undefined
    }
    const height = rowLayoutMeasurement(el)
    if (stats)
      stats.reads++
    if (height === undefined)
      return undefined
    const key = deps.currentMeasurementKey?.(id)
    const last = lastReadByEl.get(el)
    if (last !== undefined && last.id === id && last.key === key) {
      const committed = deps.committedHeight(id)
      if (committed !== undefined && Math.abs(height - committed) < MEASURE_EPSILON_PX) {
        if (stats)
          stats.skippedUnchanged++
        return undefined
      }
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
        lastReadByEl.set(el, { id, key })
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
      skippedNotReady: 0,
      skippedUnchanged: 0,
    }
    for (const el of pending) {
      const read = readMeasurement(el, stats)
      if (read)
        reads.push(read)
    }
    pending.clear()
    // Commit all measurements in one reactive batch so the offset map, the row
    // transforms, and the layout shift update once. readMeasurement already
    // dropped the unready reads, so none reaches the height cache: a row copy that
    // is not measurable (see attachRow), and a row whose layout check fails.
    stats.committed = commitReads(reads)
    deps.onFlush?.(stats)
  }

  const scheduleFlush = () => {
    if (flushScheduled)
      return
    flushScheduled = true
    schedule(flush)
  }

  /** Read `el` at the next flush. */
  const queueRead = (el: Element) => {
    pending.add(el)
    scheduleFlush()
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
      queueRead(el)
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

  const attachRow = (id: string, el: HTMLElement, readiness?: RowContentReadiness) => {
    elToId.set(el, id)
    idToEl.set(id, el)
    deps.onRowMounted(id)
    // Each attach takes a fresh read even when the previous element was the same.
    lastReadByEl.delete(el)
    if (readiness !== undefined) {
      readinessByEl.set(el, readiness)
      // A copy need not resize when its content lands, or when a body shows the
      // content that replaced the old, so its turn to measurable queues a read of
      // its own. A copy that a newer element replaced, or that left the document,
      // has nothing left to measure.
      readiness.onMeasurable(() => {
        if (idToEl.get(id) !== el || !el.isConnected)
          return
        queueRead(el)
      })
    }
    else {
      readinessByEl.delete(el)
    }
    observeImages(el)
    // Measure a mounted row immediately. The resize observer retries a read that
    // the layout check rejected, because the row resizes once it lays out.
    const read = readMeasurement(el)
    if (read)
      commitReads([read])
    ro?.observe(el)
  }

  const detachRow = (el: HTMLElement) => {
    unobserveImages(el)
    ro?.unobserve(el)
    pending.delete(el)
    readinessByEl.delete(el)
    const id = elToId.get(el)
    // Report the unmount of the id only if THIS element still owns it. Under an
    // attach-before-detach remount a new element already took the id over, so the row
    // stays mounted. A row that wrongly counted as unmounted would lose the protection
    // of its height from the eviction of the cache, and a provisional height: it would
    // then be unmeasured, and leave the admitted rows. The later detach of the newer
    // element reports the unmount.
    const unmounted = id !== undefined && idToEl.get(id) === el
    if (unmounted)
      idToEl.delete(id)
    // Drop the el->id mapping so detach is a clean inverse of attach: an element
    // later re-attached under a DIFFERENT id can't have a stale entry resolve a
    // pending measurement to the wrong id. The cached HEIGHT (keyed by id in the
    // height cache, not here) is untouched, so re-entering the row stays flash-free.
    elToId.delete(el)
    if (unmounted)
      deps.onRowUnmounted(id)
  }

  const measureAgain = (id: string) => {
    const el = idToEl.get(id)
    if (el !== undefined)
      queueRead(el)
  }

  const dispose = () => {
    for (const el of imageListeners.keys())
      unobserveImages(el)
    ro?.disconnect()
    ro = undefined
    idToEl.clear()
    pending.clear()
    // Clear the scheduled flag so a later attach can schedule another flush.
    // Disposal clears the pending reads before the queued flush runs.
    flushScheduled = false
  }

  const elementOf = (id: string): Element | undefined => idToEl.get(id)

  const hasUncommittedHeight = (id: string): boolean => {
    const el = idToEl.get(id)
    return el !== undefined && readMeasurement(el) !== undefined
  }

  return {
    attachRow,
    detachRow,
    measureAgain,
    elementOf,
    hasUncommittedHeight,
    flushNow: flush,
    dispose,
  }
}
