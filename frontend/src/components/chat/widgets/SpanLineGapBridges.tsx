import type { Component } from 'solid-js'
import type { SpanLine } from './SpanLines'
import { createMemo, For, Index, Show } from 'solid-js'
import { bodySpanKey, shouldConnectSpanLineTop, spanColorClassFor } from './SpanLines'
import { spanGapBridge, spanGapBridgeRow } from './SpanLines.css'
import { LINE_THICKNESS, SPAN_BRIDGE_GAP_VAR, spanColumnCenterX } from './SpanLines.geometry'

/**
 * The slice of a classified chat entry the bridge overlay reads. Structural
 * subset of ChatView's ClassifiedEntry, so the widget doesn't depend on the
 * entry-cache module.
 */
export interface SpanBridgeEntry {
  /** The id of the list row: the message id, or the id of a pending row (see chatPendingRows). */
  rowId: string
  message: { spanColor?: number }
  parsedSpanLines: (SpanLine | null)[]
  openingSpanId?: string | undefined
}

export interface SpanLineGapBridgesProps {
  /**
   * The rendered slice, in display order. Must be the same STABLE entry
   * references the row <For> receives, so a geometry change (which only
   * moves rows) re-runs each anchor's transform in place instead of
   * recreating the overlay DOM.
   */
  entries: readonly SpanBridgeEntry[]
  /** The entry just above the slice (range.start - 1), for the first row's connections. */
  precedingEntry: SpanBridgeEntry | undefined
  /** Live top offset of a row (the same offset map that places the rows). */
  topOf: (id: string) => number
  /**
   * Whether the row is hidden: its content is pending, an earlier row of its append
   * burst still loads, or a fling skeleton covers it. A bridge never paints for an
   * invisible row.
   */
  hiddenOf: (id: string) => boolean
  /**
   * The gap the offset map left above a row (`gapAboveOf` on the virtualizer).
   * The segments are sized from it, so this overlay and the row offsets read one
   * decider. Required, not optional, so a new mount site cannot forget it and
   * silently draw every bridge at zero height.
   */
  gapAboveOf: (id: string) => number
}

/**
 * The inter-row rail segments, drawn OUTSIDE the rows in one overlay inside
 * the virtual spacer. The rows' own span columns stop exactly at the row
 * edge; this overlay paints the continuation across the gap above each row
 * whose column connects to the row above, sized from that row's own gap (see
 * `gapAboveOf`), so a merged band pair -- which has no gap -- gets a segment of
 * zero height instead of one drawn for a gap that does not exist. Living
 * outside the rows is what
 * lets every virtualized row take `contain: layout paint` — the one thing
 * that used to overflow a row's box was this bridge segment.
 */
export const SpanLineGapBridges: Component<SpanLineGapBridgesProps> = props => (
  <For each={props.entries}>
    {(entry, index) => {
      const previous = createMemo(() => (index() > 0
        ? props.entries[index() - 1]
        : props.precedingEntry))
      const previousLines = createMemo(() => previous()?.parsedSpanLines ?? [])
      const previousBodyKey = createMemo(() => {
        const p = previous()
        return p?.openingSpanId !== undefined
          ? bodySpanKey(p.openingSpanId, p.message.spanColor)
          : undefined
      })
      // Per column: does its vertical rail continue from the row above? Memoized
      // because both the <Show> gate and the <Index> below read it each render.
      const connecting = createMemo(() => entry.parsedSpanLines.map(
        (line, col) => shouldConnectSpanLineTop(line, previousLines()[col] ?? null, previousBodyKey()),
      ))
      return (
        <Show when={entry.parsedSpanLines.length > 0 && connecting().some(Boolean)}>
          <div
            class={spanGapBridgeRow}
            style={{
              transform: `translateY(${props.topOf(entry.rowId)}px)`,
              visibility: props.hiddenOf(entry.rowId)
                ? 'hidden'
                : undefined,
              // Published once per ROW, not per column: every segment in this
              // anchor spans the same gap.
              [SPAN_BRIDGE_GAP_VAR]: `${props.gapAboveOf(entry.rowId)}px`,
            }}
            data-span-gap-bridges-for={entry.rowId}
          >
            <Index each={connecting()}>
              {(isConnecting, col) => (
                <Show when={isConnecting()}>
                  <div
                    class={`${spanGapBridge} ${spanColorClassFor(entry.parsedSpanLines[col]?.color)}`}
                    style={{ left: `${spanColumnCenterX(col) - LINE_THICKNESS / 2}px` }}
                    data-span-gap-bridge-segment={col}
                  />
                </Show>
              )}
            </Index>
          </div>
        </Show>
      )
    }}
  </For>
)
