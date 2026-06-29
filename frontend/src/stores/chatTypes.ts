/**
 * Shared chat types used by BOTH the windowing store (`chat.store` and its
 * per-concern slices -- command streams, saved viewport, ...) AND the UI
 * scroll/virtualizer (`useChatScroll`, `useChatVirtualizer`). A true LEAF: it
 * imports nothing chat-specific, so the dependency points one way
 * (consumer -> chatTypes). This is what lets the store and the virtualizer share a
 * contract -- e.g. `ScrollAnchor`, which the virtualizer PRODUCES and the store
 * PERSISTS inside `SavedViewportScroll` -- WITHOUT the store reaching up into the
 * components layer (which a type defined in the virtualizer would force).
 */

export interface CommandStreamSegment {
  kind: 'output' | 'interaction' | 'reasoning_summary' | 'reasoning_content' | 'reasoning_summary_break'
  text: string
}

/**
 * Version token for a span-linked sibling message. A row can render from its paired
 * tool_use/tool_result, so cache freshness must move when the paired side changes
 * identity, seq, or same-seq content version.
 */
export interface SpanMessageRevision {
  id: string
  seq: bigint
  contentVersion: number
}

/**
 * A scroll anchor: the message (by row id) the viewport top is pinned to, plus
 * the pixel offset from that row's top to the viewport top. The virtualizer
 * produces it (anchorAt) and resolves it back to a scrollTop (scrollTopForAnchor);
 * useChatScroll holds it across geometry changes to keep the row stationary, and
 * SavedViewportScroll persists it for tab-switch restore. Keyed by id (not seq) so
 * stacked optimistic locals (all seq 0n) stay distinct.
 */
export interface ScrollAnchor {
  id: string
  offsetWithinRow: number
  /**
   * The row's height (measured or estimated) at capture time. scrollTopForAnchor
   * resolves `offsetWithinRow` PROPORTIONALLY against this basis -- preserving the
   * fractional position into the row -- so a height change between capture and
   * resolve (an estimate that shrinks/grows while unmeasured, or a measured row
   * evicted back to an estimate) can neither truncate the offset (yanking the pin
   * up) nor overshoot the row bottom. Optional: an anchor restored from old
   * persistence without it falls back to absolute clamping.
   */
  basisHeight?: number
  /**
   * How far into the inter-row GAP below the row the viewport top sat at capture, as
   * a fraction [0, 1] of that gap (0 when the top was within the row body). Lets
   * scrollTopForAnchor reproduce a viewport parked mid-gap instead of snapping to the
   * row bottom, while staying gap-size-independent: the fraction is re-applied against
   * the CURRENT gap, so a token/span-line change that resizes the gap scales with it.
   * Optional: an anchor captured/persisted before this field falls back to 0 (the
   * prior gap-independent, pin-to-row-bottom behavior).
   */
  gapFraction?: number
  /**
   * The anchored row's message seq at capture time. Only used to recover when the
   * row was TRIMMED away before a restore resolves it (id no longer in the window):
   * the seq orders the gone anchor against the surviving rows so the restore can land
   * on the NEAREST survivor (scrollTopNearAnchor) instead of yanking the reader to the
   * live tail. Optional -- an anchor captured before this field (or for an optimistic
   * local, seq 0n) just can't do the nearest-survivor recovery and falls back.
   */
  seq?: bigint
}

/**
 * Saved scroll position for tab-switch viewport restoration. Anchored to a
 * specific message (by row id) plus the pixel offset of the viewport top within
 * that row, rather than a raw distance-from-bottom — under virtualization the
 * scroll container's height is partly estimated, so a distance would resolve
 * to the wrong place. Keyed by id (not seq) so stacked optimistic locals (all
 * seq 0n) restore to the right row. `atBottom`/`hasMoreNewer` let restore decide
 * whether to stick to the live tail or resolve the anchor.
 */
export interface SavedViewportScroll {
  /**
   * The anchored viewport-top row (id + pixel offset within it), or undefined
   * when there was nothing to anchor to -- the window was at the bottom or empty.
   * A real `ScrollAnchor` instead of a flattened id + sentinel `''`, so "no
   * anchor" is `undefined` the type enforces rather than a magic empty string.
   */
  anchor?: ScrollAnchor
  /**
   * Fallback raw scrollTop, set ONLY when scrolled away from the bottom with no
   * resolvable row anchor -- a window momentarily composed of only hidden rows,
   * where the virtual list is empty (totalHeight 0) so there is no estimated
   * spacer for a pixel offset to drift against. Lets restore return to that
   * position instead of snapping to the live tail; ignored when `anchor` resolves.
   */
  rawScrollTop?: number
  /** Whether the viewport was pinned to the bottom when saved. */
  atBottom: boolean
  /** Whether the window was scrolled away from the live tail when saved. */
  hasMoreNewer: boolean
}
