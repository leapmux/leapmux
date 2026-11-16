import type { ChatRowItem } from './chatRowItem'
import type { UseChatVirtualizerOptions } from './useChatVirtualizer'
import { createSignal, onCleanup } from 'solid-js'
import { onTestFinished, vi } from 'vitest'
import { createRowHeightCache } from './chatRowHeightCache'
import { useChatVirtualizer } from './useChatVirtualizer'

/**
 * Shared test kit for the useChatVirtualizer suites. The geometry suite was split
 * out of a single ~1.1k-line file into per-area spec files; these item builders,
 * the fake DOM row, and the deterministic `setup` are the common prelude every
 * split imports.
 */

export function makeItems(specs: Array<{ seq: number, span?: boolean, kind?: string }>): ChatRowItem[] {
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

export function plainItems(count: number, startSeq = 1): ChatRowItem[] {
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

/** The report of a row of the list with no height. The first group is the row id. */
const UNMEASURED_ROW_REPORT = /^useChatVirtualizer: the row (.+) is in the list with no measured height\./

/**
 * Spy on `console.error`, where a development build of the virtualizer reports a row of the
 * list with no height. The app never lists such a row, so a test that lists one on purpose
 * expects the report. The spy keeps the reports out of the test output, and ends with the
 * test. `reportedIds` gives the row id of each report, in order. For any other error, it
 * gives the whole message, so that an unexpected error fails the comparison.
 */
export function spyOnUnmeasuredRowReports(): { reportedIds: () => string[] } {
  const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
  onTestFinished(() => spy.mockRestore())
  return {
    reportedIds: () => spy.mock.calls.map(([message]) => {
      const text = String(message)
      return UNMEASURED_ROW_REPORT.exec(text)?.[1] ?? text
    }),
  }
}

export interface VirtualizerSetupOptions extends Pick<UseChatVirtualizerOptions, 'scrollElement' | 'pinBottom' | 'spacerInsetPx' | 'overscanPx' | 'listStartsAtLoadedTop'> {
  /**
   * The height (px) that the kit commits for every row at the start, as a hidden copy
   * does, and for each row that `setList` adds. Default: 100, so the rows are 100px tall
   * and row `i` starts at 120·i with the gap of 20. `false`: the kit commits no height. A
   * test then commits each height through the cache before the row enters the list, or
   * expects the report of a row with no height (see spyOnUnmeasuredRowReports).
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
export function setup(items: ChatRowItem[], opts: VirtualizerSetupOptions = {}) {
  const { measured = 100, ...virtualizerOptions } = opts
  // The kit protects no row as loaded: an eviction test expects the eviction to drop the
  // oldest heights, also the height of a row of the list.
  const cache = createRowHeightCache({
    loadedIds: () => [],
  })
  // Commit the height for each row that the cache does not know yet. A row that the cache
  // knows keeps its entry, also under another key, so a test of a key change sees it.
  const measureUnknown = (rows: readonly ChatRowItem[]): void => {
    if (measured === false)
      return
    cache.commitAll(rows
      .filter(row => cache.debugEntry(row.id) === undefined)
      .map(row => ({ row, height: measured })))
  }
  measureUnknown(items)
  const [list, setListSignal] = createSignal(items)
  const setList = (next: ChatRowItem[]): void => {
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
