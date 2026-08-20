import type { Component } from 'solid-js'
import { For } from 'solid-js'

/**
 * Nine small rounded squares in a 3x3 block, each filled independently.
 *
 * A "pip" is the mark on a die face, which is what one square is here. The word
 * is deliberate: `tile` names a pane of the tab layout throughout this codebase,
 * and `cell` names a terminal cell, so neither could describe this without
 * asking the reader which meaning applies.
 *
 * Geometry only -- it carries no meaning of its own and no tooltip. Both
 * consumers supply the meaning: `ContextUsageGrid` reads the fills as a meter,
 * and `ThemeSwatch` reads them as a palette.
 */

/** The edge of one pip, in viewBox units. */
const PIP = 3
/** The space between two pips, in viewBox units. */
const GAP = 1
/** Distance from one pip's edge to the next one's, in viewBox units. */
const STEP = PIP + GAP
/** Pips per row, and per column. */
export const PIP_GRID_COLUMNS = 3
/** How many fills `PipGrid` expects. */
export const PIP_GRID_PIPS = PIP_GRID_COLUMNS * PIP_GRID_COLUMNS
/**
 * The viewBox edge: three pips and the two gaps between them.
 *
 * There is no outer padding, so a pip touches each edge of the box. A caller
 * that wants space around the block adds CSS padding to the element it puts
 * this in -- which is what gives `ThemeSwatch` the ring of palette background
 * around its pips.
 */
const EXTENT = PIP_GRID_COLUMNS * PIP + (PIP_GRID_COLUMNS - 1) * GAP

/** Row-major indices, so `<For>` can map an index straight to a position. */
const POSITIONS = Array.from(
  { length: PIP_GRID_PIPS },
  (_, i) => [Math.floor(i / PIP_GRID_COLUMNS), i % PIP_GRID_COLUMNS] as const,
)

interface PipGridProps {
  /**
   * Exactly nine fills, row-major: `[r0c0, r0c1, r0c2, r1c0, ...]`.
   *
   * Any CSS colour, including a `var()`. A caller with a different fill order
   * maps it to row-major before it gets here.
   */
  fills: readonly string[]
  /** Rendered edge length in px. Omit to let `class` size the SVG. */
  size?: number
  class?: string
  testId?: string
}

export const PipGrid: Component<PipGridProps> = (props) => {
  return (
    <svg
      width={props.size}
      height={props.size}
      viewBox={`0 0 ${EXTENT} ${EXTENT}`}
      fill="none"
      class={props.class}
      data-testid={props.testId}
    >
      <For each={POSITIONS}>
        {([row, col], i) => (
          <rect
            x={col * STEP}
            y={row * STEP}
            width={PIP}
            height={PIP}
            rx={0.5}
            // `transparent` rather than nothing for a fill this was not given:
            // an absent `fill` attribute makes SVG paint the pip BLACK, which
            // reads as a colour the caller chose. A see-through pip reads as
            // the gap it is.
            fill={props.fills[i()] ?? 'transparent'}
          />
        )}
      </For>
    </svg>
  )
}
