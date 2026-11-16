/**
 * The descriptor of a loaded row: the height cache, the admission, the staging units, and
 * the virtualizer read it. `id` keys the height
 * cache, the offset map, and the scroll anchor. It is stable across window
 * trims and unique per row. A sequence can change when the worker consolidates
 * a notification. `hasSpanLines` drives the inter-row gap (span-line rows sit closer
 * together so their vertical rails bridge — see SpanLines.css.ts).
 */
export interface ChatRowItem {
  id: string
  hasSpanLines: boolean
  /**
   * The row's message seq, recorded onto a captured ScrollAnchor so a trimmed-away
   * anchor can be ordered against the surviving rows for the nearest-survivor restore
   * (scrollTopNearAnchor). Optional: a caller that doesn't supply it just can't do
   * that recovery (the offset map and slicing never read it -- ordering is positional).
   */
  seq?: bigint
  /**
   * Per-row geometry version for cached DOM heights.
   * A stable id can change content, width-sensitive layout, or UI state in place; this
   * key changes in those cases so stale measured/premeasured heights are ignored.
   * Absent -> id-only caching.
   */
  heightKey?: string
  /**
   * The message's rendering kind (category.kind: 'user_text', 'tool_result', ...). Two
   * adjacent band rows share one border (see `gapAfter`), and the diagnostic of a height
   * commit records it. Reuses the entry's EXISTING classification (ChatView passes
   * `category.kind`), never re-classifying.
   */
  kind?: string
  /**
   * True for a row that continues the row above it: the list leaves no gap above it, and
   * the row's own content holds its top space. The pending row of a tool call is one (see
   * chatPendingRows): while it has nothing to show, it takes no height and no gap.
   */
  continuesPrevious?: boolean
}

/**
 * Classifies EVERY ChatRowItem field as geometry-affecting (true -> compared by
 * sameChatRowItems / feeds the offset map, gap, and measured-height cache) or not. The
 * `Record<keyof Required<ChatRowItem>, boolean>` annotation forces a COMPILE error when a
 * field is added to ChatRowItem without classifying it here, closing the "forgot to update
 * the equality" hazard. `seq` (anchor label) is NOT geometry -- it does not feed the offset
 * map.
 */
const GEOMETRY_RELEVANCE: Record<keyof Required<ChatRowItem>, boolean> = {
  id: true,
  hasSpanLines: true,
  heightKey: true,
  seq: false,
  // Decides the gap between two adjacent bands, so a kind change shifts the offset map.
  // In practice a kind flip also bumps heightKey (both track the same reclassification),
  // so this rarely forces an extra rebuild on its own -- but classifying it as geometry
  // keeps the offset map correct even if that coupling ever loosens.
  kind: true,
  // Decides the gap above the row.
  continuesPrevious: true,
}

/** The geometry-affecting ChatRowItem keys, derived once (module load) from the above. */
const GEOMETRY_KEYS = (Object.keys(GEOMETRY_RELEVANCE) as (keyof ChatRowItem)[])
  .filter(k => GEOMETRY_RELEVANCE[k])

/**
 * Whether two arrays of row items are GEOMETRY-EQUIVALENT: same length and the same
 * geometry fields (GEOMETRY_KEYS = id, hasSpanLines, heightKey, kind) at every index. The
 * virtualizer keys its offset map, inter-row gap, and measured-height cache on exactly
 * those fields, so a recompute that preserves them produces an
 * identical offset map. It is the `equals` of two memos of ChatView: `loadedItems` (the
 * loaded rows) and `virtualItems` (the admitted rows). A recompute that does not add,
 * remove, reorder, or revise a row keeps the prior array reference. That stops an
 * unnecessary geometry rebuild and layout-shift pass. Row content still updates through
 * visibleEntries. Row measurement catches a real height change.
 *
 * MAINTENANCE: the compared fields are DERIVED from GEOMETRY_RELEVANCE above (compile-
 * enforced exhaustive over ChatRowItem's keys), so a new geometry-affecting field can't be
 * silently omitted from the equality and leave the offset map stale.
 */
export function sameChatRowItems(a: ChatRowItem[], b: ChatRowItem[]): boolean {
  if (a === b)
    return true
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (x === undefined || y === undefined)
      return false
    for (const k of GEOMETRY_KEYS) {
      if (x[k] !== y[k])
        return false
    }
  }
  return true
}
