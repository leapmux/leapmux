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
 * Extension to bridge vertical lines across the gap between message rows.
 * Span-line rows sit a tightened --space-2 apart (the virtualizer encodes this
 * gap directly in each row's offset; see useChatVirtualizer's gapSmallPx).
 */
export const ROW_GAP = 'var(--space-2)'

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
