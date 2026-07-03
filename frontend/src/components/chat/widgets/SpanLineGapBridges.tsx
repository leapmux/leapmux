import type { Component } from 'solid-js'
import type { SpanLine } from './SpanLines'
import { createMemo, For, Index, Show } from 'solid-js'
import { bodySpanKey, shouldConnectSpanLineTop, spanColorClassFor } from './SpanLines'
import { spanGapBridge, spanGapBridgeRow } from './SpanLines.css'
import { LINE_THICKNESS, spanColumnCenterX } from './SpanLines.geometry'

/**
 * The slice of a classified chat entry the bridge overlay reads. Structural
 * subset of ChatView's ClassifiedEntry, so the widget doesn't depend on the
 * entry-cache module.
 */
export interface SpanBridgeEntry {
  msg: { id: string, spanId?: string, spanColor?: number }
  parsedSpanLines: (SpanLine | null)[]
  category: { kind: string }
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
  /** Mirrors the row's hide-until-measured state so a bridge never paints for an invisible row. */
  hiddenOf: (id: string) => boolean
}

/**
 * The inter-row rail segments, drawn OUTSIDE the rows in one overlay inside
 * the virtual spacer. The rows' own span columns stop exactly at the row
 * edge; this overlay paints the ROW_GAP-tall continuation above each row
 * whose column connects to the row above. Living outside the rows is what
 * lets every virtualized row take `contain: layout paint` — the one thing
 * that used to overflow a row's box was this bridge segment.
 */
export const SpanLineGapBridges: Component<SpanLineGapBridgesProps> = props => (
  <For each={props.entries}>
    {(entry, index) => {
      const previous = createMemo(() => (index() > 0 ? props.entries[index() - 1] : props.precedingEntry))
      const previousLines = createMemo(() => previous()?.parsedSpanLines ?? [])
      const previousBodyKey = createMemo(() => {
        const p = previous()
        return p?.category.kind === 'tool_use' ? bodySpanKey(p.msg.spanId, p.msg.spanColor) : undefined
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
              transform: `translateY(${props.topOf(entry.msg.id)}px)`,
              visibility: props.hiddenOf(entry.msg.id) ? 'hidden' : undefined,
            }}
            data-span-gap-bridges-for={entry.msg.id}
          >
            <Index each={connecting()}>
              {(isConnecting, col) => (
                <Show when={isConnecting()}>
                  <div
                    class={`${spanGapBridge} ${spanColorClassFor(entry.parsedSpanLines[col]?.color)}`}
                    style={{ left: `${spanColumnCenterX(col) - LINE_THICKNESS / 2}px` }}
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
