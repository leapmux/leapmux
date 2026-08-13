import { CHAT_PAD_LEFT_VAR } from '../chatChromeVars'

/** Thickness of all span lines (vertical, horizontal, connectors, bridges). */
export const LINE_THICKNESS = 2

/** Center-to-center spacing between adjacent span columns. */
export const COL_SPACING = 19

/** Rendered element width of each column (line centered at COL_WIDTH / 2). */
export const COL_WIDTH = 24

/** Overlap between adjacent columns (applied as negative left margin). */
export const COL_OVERLAP = COL_WIDTH - COL_SPACING // 5

/** Vertical offset aligning horizontal connectors with the first text line. */
export const CONNECTOR_Y = 9

/** Inset from the column's right edge to the connector tip. */
export const CONNECTOR_GAP = 4

/** Right padding on the SpanLines container. */
export const CONTAINER_PAD_RIGHT = 1

/**
 * Custom property carrying the gap the offset map left ABOVE a row.
 *
 * The bridge overlay publishes it per row from `gapAboveOf`
 * (~/components/chat/useChatVirtualizer.ts) and `./SpanLines.css.ts` sizes the
 * segment from it, so ONE function decides both a row's offset and the height of
 * the rail that spans the space above it. The bridge used to restate that gap as
 * its own token, which held only while every gap was the same: two adjacent
 * BANDS overlap by a border width instead, and a segment sized from a token was
 * built for a gap that does not exist. Reading the decider makes it collapse.
 */
export const SPAN_BRIDGE_GAP_VAR = '--span-bridge-gap'

/** Diameter of the bridge arc. */
export const BRIDGE_DIAMETER = 10

/** Radius of the bridge arc. */
export const BRIDGE_RADIUS = BRIDGE_DIAMETER / 2 // 5

/** Top edge of the bridge arc. */
export const BRIDGE_TOP = 6

/** Bottom edge of the bridge arc (center of the connector line). */
export const BRIDGE_BOTTOM = CONNECTOR_Y + LINE_THICKNESS / 2 // 10

/** Overlap so horizontal segments tuck under bridge borders (sub-pixel gap fix). */
export const BRIDGE_SEAM = 1

/** Left margin for tool body content borders (matches column overlap). */
export const TOOL_BODY_INDENT = COL_OVERLAP // 5

/** Left margin for messages without span lines. */
export const NO_SPAN_MARGIN = CONTAINER_PAD_RIGHT // 1

/** Width reserved by the rendered span-line column stack, without drawing it. */
export function spanLinesReservedWidth(lineCount: number): number {
  if (lineCount <= 0)
    return NO_SPAN_MARGIN
  return lineCount * COL_SPACING + CONTAINER_PAD_RIGHT
}

/**
 * Custom property holding the distance from a row-content element's own left
 * edge to the PANEL's left edge: the chat list's gutter plus whatever the span
 * rails reserve to its left.
 *
 * A bleeding descendant (a turn-end rule, a band) cannot use the gutter alone,
 * because the rails push it right by a width only the row knows. Every element
 * that starts a row's content column publishes this, so one negative margin
 * reaches the panel edge whether the row has rails or not.
 */
export const ROW_BLEED_LEFT_VAR = '--row-bleed-left'

/** The `ROW_BLEED_LEFT_VAR` declaration for a row with `lineCount` rails. */
export function rowBleedLeftStyle(lineCount: number): Record<string, string> {
  return { [ROW_BLEED_LEFT_VAR]: `calc(var(${CHAT_PAD_LEFT_VAR}, 0px) + ${spanLinesReservedWidth(lineCount)}px)` }
}

/**
 * Content-column style for a row that RESERVES the rails' width instead of
 * rendering the rails: the hidden premeasure row, and the live row that has no
 * rails at all. Emits the left margin and ROW_BLEED_LEFT_VAR together, from one
 * `lineCount`, so the two cannot fall out of step -- a column whose margin and
 * whose published distance disagree wraps its text at a width the other row never
 * reproduces, and the wrong height reaches the offset map.
 *
 * A row that RENDERS its rails needs no margin: the `SpanLines` element occupies
 * that width as a flex sibling. It publishes the var alone.
 */
export function reservedRowContentColumnStyle(lineCount: number): Record<string, string> {
  return { 'margin-left': `${spanLinesReservedWidth(lineCount)}px`, ...rowBleedLeftStyle(lineCount) }
}

/**
 * X position (px, from the row's left edge) of a span column's line center.
 * The first column's box starts at -COL_OVERLAP (every column carries the
 * negative overlap margin, including the first), so center i sits at
 * COL_WIDTH/2 - COL_OVERLAP + i * COL_SPACING. Used by the gap-bridge overlay
 * (SpanLineGapBridges) to draw the inter-row rail segments at exactly the
 * in-row line positions.
 */
export function spanColumnCenterX(index: number): number {
  return COL_WIDTH / 2 - COL_OVERLAP + index * COL_SPACING
}
