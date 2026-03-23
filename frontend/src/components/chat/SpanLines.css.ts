import { globalStyle, style, styleVariants } from '@vanilla-extract/css'

// ─── Span Column Geometry ───────────────────────────────────────────
//
// Span lines visualize nested tool invocations as colored vertical
// lines in columns to the left of message content.  Horizontal
// connectors (├ and └ shapes) link a vertical line to the message
// that opens or closes a span.
//
//     ←──── COL_WIDTH (24) ────→
//     ┌──────────┬─────────────┐
//     │     line (2px)         │
//     │      ┃   │             │
//     │      ┃────── connector ──→│← CONNECTOR_GAP (4) →│ message
//     │      ┃   │             │                        │
//     └──────────┴─────────────┘                        │
//     ←overlap→                                         │
//       (5px)  ┌─── next col ───┐                       │
//
// Adjacent columns overlap by COL_OVERLAP so their centers sit
// COL_WIDTH − COL_OVERLAP = COL_SPACING apart.

/** Thickness of all span lines (vertical, horizontal, connectors, bridges). */
export const LINE_THICKNESS = 2

/** Center-to-center spacing between adjacent span columns. */
const COL_SPACING = 19

/** Rendered element width of each column (line centered at COL_WIDTH / 2). */
const COL_WIDTH = 24

/** Overlap between adjacent columns (applied as negative left margin). */
const COL_OVERLAP = COL_WIDTH - COL_SPACING // 5

// ─── Connector Positioning ──────────────────────────────────────────

/** Vertical offset aligning horizontal connectors with the first text line. */
const CONNECTOR_Y = 9

/** Inset from the column's right edge to the connector tip. */
const CONNECTOR_GAP = 4

// ─── Container Spacing ─────────────────────────────────────────────

/** Right padding on the SpanLines container. */
const CONTAINER_PAD_RIGHT = 1

/**
 * Non-opener containers (no horizontal connector) are pulled tighter
 * by this amount so vertical-only lines sit closer to message content.
 */
const NON_OPENER_TIGHTEN = 4

/** Extension to bridge vertical lines across the gap between message rows. */
const ROW_GAP = 'var(--space-3)'

// ─── Bridge (Passthrough Hop) ───────────────────────────────────────
//
// When a horizontal connector passes through a column that already has
// a vertical line, a bridge arc hops over the vertical line to avoid
// visual ambiguity.

/** Diameter of the bridge arc. */
const BRIDGE_DIAMETER = 10

/** Radius of the bridge arc. */
const BRIDGE_RADIUS = BRIDGE_DIAMETER / 2 // 5

/** Top edge of the bridge arc. */
const BRIDGE_TOP = 6

/** Bottom edge of the bridge arc (center of the connector line). */
const BRIDGE_BOTTOM = CONNECTOR_Y + LINE_THICKNESS / 2 // 10

/** Overlap so horizontal segments tuck under bridge borders (sub-pixel gap fix). */
const BRIDGE_SEAM = 1

// ─── Derived Exports (for toolStyles.css.ts, ChatView.tsx) ──────────

/** Left margin for tool body content borders (matches column overlap). */
export const TOOL_BODY_INDENT = COL_OVERLAP // 5

/** Left margin for messages without span lines. */
export const NO_SPAN_MARGIN = CONTAINER_PAD_RIGHT // 1

// ─── Styles ─────────────────────────────────────────────────────────

/** Container for all span line columns. */
const spanLinesContainerBase = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'stretch',
  flexShrink: 0,
  paddingRight: `${CONTAINER_PAD_RIGHT}px`,
})

export const spanLinesContainer = style([spanLinesContainerBase])

export const spanLinesContainerSpanOpener = style([spanLinesContainerBase])

/** Base style for a single span line column. */
const spanLineColumnBase = style({
  width: `${COL_WIDTH}px`,
  marginLeft: `${-COL_OVERLAP}px`,
  position: 'relative',
  flexShrink: 0,
})

/** Vertical line running through the column center. */
export const spanLineActive = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${ROW_GAP})`,
    bottom: `calc(-1 * ${ROW_GAP})`,
    width: `${LINE_THICKNESS}px`,
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Empty column (no active span). */
export const spanLineEmpty = style([spanLineColumnBase])

/** Horizontal connector from the vertical line to the message (├ shape). */
export const spanLineConnector = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${ROW_GAP})`,
    bottom: `calc(-1 * ${ROW_GAP})`,
    width: `${LINE_THICKNESS}px`,
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `${CONNECTOR_Y}px`,
    right: `${CONNECTOR_GAP}px`,
    height: `${LINE_THICKNESS}px`,
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Bottom-corner connector (└ shape): vertical line down to connector, then horizontal. */
export const spanLineConnectorEnd = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${ROW_GAP})`,
    height: `calc(${CONNECTOR_Y + LINE_THICKNESS}px + ${ROW_GAP})`,
    width: `${LINE_THICKNESS}px`,
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `${CONNECTOR_Y}px`,
    right: `${CONNECTOR_GAP}px`,
    height: `${LINE_THICKNESS}px`,
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/**
 * Build the horizontal passthrough gradient with a gap for the bridge arc.
 * @param rightEnd CSS value for where the right segment ends
 */
function passthroughGradient(rightEnd: string) {
  const c = 'var(--span-passthrough-color, var(--border))'
  const gapL = `calc(50% - ${BRIDGE_RADIUS - BRIDGE_SEAM}px)`
  const gapR = `calc(50% + ${BRIDGE_RADIUS - BRIDGE_SEAM}px)`
  return `linear-gradient(to right, ${c} 0, ${c} ${gapL}, transparent ${gapL}, transparent ${gapR}, ${c} ${gapR}, ${c} ${rightEnd}${rightEnd !== '100%' ? `, transparent ${rightEnd}` : ''})`
}

/** Active vertical line with a horizontal pass-through that hops over the vertical line. */
export const spanLineActivePassthrough = style([spanLineColumnBase, {
  'backgroundImage': passthroughGradient('100%'),
  'backgroundSize': `100% ${LINE_THICKNESS}px`,
  'backgroundPosition': `0 ${CONNECTOR_Y}px`,
  'backgroundRepeat': 'no-repeat',
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${ROW_GAP})`,
    bottom: `calc(-1 * ${ROW_GAP})`,
    width: `${LINE_THICKNESS}px`,
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    boxSizing: 'border-box',
    left: '50%',
    top: `${BRIDGE_TOP}px`,
    width: `${BRIDGE_DIAMETER}px`,
    height: `${BRIDGE_BOTTOM - BRIDGE_TOP + BRIDGE_SEAM}px`,
    transform: 'translateX(-50%)',
    borderLeft: `${LINE_THICKNESS}px solid var(--span-passthrough-color, var(--border))`,
    borderRight: `${LINE_THICKNESS}px solid var(--span-passthrough-color, var(--border))`,
    borderTop: `${LINE_THICKNESS}px solid var(--span-passthrough-color, var(--border))`,
    borderBottom: 'none',
    borderRadius: `${BRIDGE_RADIUS}px ${BRIDGE_RADIUS}px 0 0`,
  },
}])

/** Empty column with a horizontal pass-through line. */
export const spanLinePassthrough = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: 0,
    right: 0,
    top: `${CONNECTOR_Y}px`,
    height: `${LINE_THICKNESS}px`,
    backgroundColor: 'var(--span-passthrough-color, var(--border))',
  },
}])

/** Shorten the rightmost passthrough column so it doesn't touch the message content. */
globalStyle(`${spanLineActivePassthrough}:last-child`, {
  backgroundImage: passthroughGradient(`calc(100% - ${CONNECTOR_GAP}px)`),
})
globalStyle(`${spanLinePassthrough}:last-child::before`, {
  right: `${CONNECTOR_GAP}px`,
})

/** Span line color palette (cycled by color index). */
const PALETTE = [
  'rgb(59 130 246)',
  'rgb(34 197 94)',
  'rgb(249 115 22)',
  'rgb(168 85 247)',
  'rgb(236 72 153)',
  'rgb(20 184 166)',
  'rgb(234 179 8)',
  'rgb(239 68 68)',
]

export const spanLineColors = styleVariants(
  Object.fromEntries(PALETTE.map((color, i) => [`color${i}`, { vars: { '--span-line-color': color } }])),
)

export const spanPassthroughColors = styleVariants(
  Object.fromEntries(PALETTE.map((color, i) => [`color${i}`, { vars: { '--span-passthrough-color': color } }])),
)

export const PALETTE_SIZE = PALETTE.length
