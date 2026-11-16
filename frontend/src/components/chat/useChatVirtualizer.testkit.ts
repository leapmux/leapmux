import type { UseChatVirtualizerOptions, VirtualItem } from './useChatVirtualizer'
import { createSignal, onCleanup } from 'solid-js'
import { createRowHeightCache } from './chatRowHeightCache'
import { useChatVirtualizer } from './useChatVirtualizer'

/**
 * Shared test kit for the useChatVirtualizer suites. The geometry suite was split
 * out of a single ~1.1k-line file into per-area spec files; these item builders,
 * the fake DOM row, and the deterministic `setup` are the common prelude every
 * split imports.
 */

export function makeItems(specs: Array<{ seq: number, span?: boolean, kind?: string }>): VirtualItem[] {
  // `seq` is only a convenient way to derive a unique row id in these specs; the
  // virtualizer itself keys everything by `id`. `kind` is omitted unless a spec
  // sets it.
  return specs.map(s => ({
    id: `m${s.seq}`,
    hasSpanLines: !!s.span,
    ...(s.kind === undefined
      ? {}
      : { kind: s.kind }),
  }))
}

export function plainItems(count: number, startSeq = 1): VirtualItem[] {
  return makeItems(Array.from({ length: count }, (_, i) => ({ seq: startSeq + i })))
}

/** Mount a row with a controlled layout. Remove it when its owner ends. */
export function fakeRow(h: number): HTMLElement {
  const el = document.createElement('div')
  el.getBoundingClientRect = () => new DOMRect(0, 0, 400, h)
  document.body.append(el)
  onCleanup(() => el.remove())
  return el
}

/** Mount a row with no layout yet: a read of it gives no height. */
export function noLayoutRow(): HTMLElement {
  const el = fakeRow(0)
  el.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
  return el
}

export interface VirtualizerSetupOptions extends Pick<UseChatVirtualizerOptions, 'scrollElement' | 'pinBottom' | 'spacerInsetPx' | 'overscanPx'> {
  /**
   * The height (px) that the kit commits for every row at the start, as a hidden copy
   * does, and for each row that `setList` adds. Default: 100, so the rows are 100px tall
   * and row `i` starts at 120·i with the gap of 20. `false`: no row has a height.
   */
  measured?: number | false
}

/**
 * Build a virtualizer with deterministic geometry for math assertions, over a height cache
 * of its own, which it returns. The list holds measured rows only in the app, so the kit
 * measures every row (see VirtualizerSetupOptions.measured). `opts` passes a scroll
 * element, a bottom pin, and the inset of the spacer on, for the layout-shift tests.
 * It also sets the overscan (px) of the rendered slice. Default: 0, so the slice holds
 * the view alone.
 */
export function setup(items: VirtualItem[], opts: VirtualizerSetupOptions = {}) {
  const { measured = 100, ...virtualizerOptions } = opts
  // The kit protects no row as loaded: an eviction test fills the cache with rows of the
  // list, and expects the eviction to drop the oldest of them.
  const cache = createRowHeightCache({
    loadedIds: () => [],
  })
  // Commit the height for each row that the cache does not know yet. A row that the cache
  // knows keeps its entry, also under another key, so a test of a key change sees it.
  const measureUnknown = (rows: readonly VirtualItem[]): void => {
    if (measured === false)
      return
    cache.commitAll(rows
      .filter(row => cache.debugEntry(row.id) === undefined)
      .map(row => ({ row, height: measured })))
  }
  measureUnknown(items)
  const [list, setListSignal] = createSignal(items)
  const setList = (next: VirtualItem[]): void => {
    measureUnknown(next)
    setListSignal(next)
  }
  const virt = useChatVirtualizer({
    items: list,
    heightCache: cache,
    overscanPx: 0,
    gapSmallPx: 10,
    gapLargePx: 20,
    ...virtualizerOptions,
  })
  return { virt, setList, cache }
}
