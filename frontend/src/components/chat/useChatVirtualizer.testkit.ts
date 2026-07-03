import type { VirtualItem } from './useChatVirtualizer'
import { createSignal } from 'solid-js'
import { useChatVirtualizer } from './useChatVirtualizer'

/**
 * Shared test kit for the useChatVirtualizer suites. The geometry suite was split
 * out of a single ~1.1k-line file into per-area spec files; these item builders,
 * the fake DOM row, and the deterministic `setup` are the common prelude every
 * split imports.
 */

export function makeItems(specs: Array<{ seq: number, span?: boolean }>): VirtualItem[] {
  // `seq` is only a convenient way to derive a unique row id in these specs; the
  // virtualizer itself keys everything by `id`.
  return specs.map(s => ({ id: `m${s.seq}`, hasSpanLines: !!s.span }))
}

export function plainItems(count: number, startSeq = 1): VirtualItem[] {
  return makeItems(Array.from({ length: count }, (_, i) => ({ seq: startSeq + i })))
}

/** A detached DOM row whose measured height is `h` (jsdom reports 0 otherwise). */
export function fakeRow(h: number): HTMLElement {
  const el = document.createElement('div')
  el.getBoundingClientRect = () => ({ height: h }) as DOMRect
  return el
}

/** Build a virtualizer with deterministic geometry for math assertions. */
export function setup(items: VirtualItem[]) {
  const [list, setList] = createSignal(items)
  const virt = useChatVirtualizer({
    items: list,
    overscanPx: 0,
    estimateHeight: 100,
    gapSmallPx: 10,
    gapLargePx: 20,
  })
  return { virt, setList }
}
