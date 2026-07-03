import { globalStyle, style, styleVariants } from '@vanilla-extract/css'
import { darkTerminalTheme, lightTerminalTheme } from '~/lib/terminal'
import {
  BRIDGE_BOTTOM,
  BRIDGE_DIAMETER,
  BRIDGE_RADIUS,
  BRIDGE_SEAM,
  BRIDGE_TOP,
  COL_OVERLAP,
  COL_WIDTH,
  CONNECTOR_GAP,
  CONNECTOR_Y,
  CONTAINER_PAD_RIGHT,
  LINE_THICKNESS,
  ROW_GAP,
} from './SpanLines.geometry'

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
//
// When a horizontal connector passes through a column that already has
// a vertical line, a bridge arc hops over the vertical line to avoid
// visual ambiguity.
//
// Geometry constants live in SpanLines.geometry.ts so runtime helpers can
// share the same math without exporting functions from this CSS module.

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
    top: 0,
    bottom: 0,
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
    top: 0,
    bottom: 0,
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
    top: 0,
    height: `${CONNECTOR_Y + LINE_THICKNESS}px`,
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
    top: 0,
    bottom: 0,
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

/**
 * Span line color palette (cycled by color index).
 * Uses the same Dimidium color scheme as the xterm.js terminal themes
 * defined in terminal.ts, with separate palettes for light and dark modes.
 */
function pickSpanColors(theme: { blue?: string, green?: string, brightRed?: string, magenta?: string, brightMagenta?: string, cyan?: string, yellow?: string, red?: string }): string[] {
  return [
    theme.blue!,
    theme.green!,
    theme.brightRed!, // orange-ish
    theme.magenta!,
    theme.brightMagenta!,
    theme.cyan!,
    theme.yellow!,
    theme.red!,
  ]
}

const DARK_PALETTE = pickSpanColors(darkTerminalTheme)
const LIGHT_PALETTE = pickSpanColors(lightTerminalTheme)
export const PALETTE_SIZE = DARK_PALETTE.length

// Generate palette CSS custom properties for theme switching.
// Each color index gets a --span-palette-N variable that changes with data-theme.
const paletteIndices = Array.from({ length: PALETTE_SIZE }, (_, i) => i)
globalStyle(':root', {
  vars: Object.fromEntries(paletteIndices.map(i => [`--span-palette-${i}`, LIGHT_PALETTE[i]])),
})
globalStyle('[data-theme="dark"]', {
  vars: Object.fromEntries(paletteIndices.map(i => [`--span-palette-${i}`, DARK_PALETTE[i]])),
})

export const spanLineColors = styleVariants(
  Object.fromEntries(paletteIndices.map(i => [`color${i}`, { vars: { '--span-line-color': `var(--span-palette-${i})` } }])),
)

// ─── Gap-bridge overlay ─────────────────────────────────────────────
//
// The rail segments that cross the inter-row gap live OUTSIDE the rows, in
// one absolutely-positioned overlay (SpanLineGapBridges) inside the virtual
// spacer. Keeping them out of the rows is what lets every virtualized row
// take `contain: layout paint` — with the old in-row top-overhang, paint
// containment would have clipped the bridges at the row edge.

/**
 * Zero-height anchor positioned at a row's top offset (translateY, like the
 * row itself); its bridge children extend upward into the gap.
 */
export const spanGapBridgeRow = style({
  position: 'absolute',
  top: 0,
  left: 0,
  right: 0,
  height: 0,
  pointerEvents: 'none',
})

/**
 * One vertical rail segment across the inter-row gap. Overlaps the row below
 * by BRIDGE_SEAM so a fractional translateY can't show a hairline seam where
 * the in-row line takes over (same trick the bridge arc uses).
 */
export const spanGapBridge = style({
  position: 'absolute',
  top: `calc(-1 * ${ROW_GAP})`,
  height: `calc(${ROW_GAP} + ${BRIDGE_SEAM}px)`,
  width: `${LINE_THICKNESS}px`,
  backgroundColor: 'var(--span-line-color, var(--border))',
})

export const spanPassthroughColors = styleVariants(
  Object.fromEntries(paletteIndices.map(i => [`color${i}`, { vars: { '--span-passthrough-color': `var(--span-palette-${i})` } }])),
)
