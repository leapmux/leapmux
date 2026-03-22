import { style, styleVariants } from '@vanilla-extract/css'

/** Container for all span line columns. */
const spanLinesContainerBase = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'stretch',
  flexShrink: 0,
})

export const spanLinesContainer = style([spanLinesContainerBase, {
  marginRight: '-4px',
}])

export const spanLinesContainerSpanOpener = style([spanLinesContainerBase])

/** Base style for a single span line column. */
const spanLineColumnBase = style({
  width: '24px',
  marginLeft: '-5px',
  position: 'relative',
  flexShrink: 0,
})

/** Extension to bridge the gap between message rows. */
const GAP_EXTEND = 'var(--space-3)'

/** Vertical line running through the column center. */
export const spanLineActive = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${GAP_EXTEND})`,
    bottom: `calc(-1 * ${GAP_EXTEND})`,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Empty column (no active span). */
export const spanLineEmpty = style([spanLineColumnBase])

/** Vertical offset for horizontal connector lines — aligns to center of first text line. */
const CONNECTOR_TOP = '9px'
/** Gap between the connector tip and the message content. */
const CONNECTOR_GAP = '4px'

/** Horizontal connector from the vertical line to the message. */
export const spanLineConnector = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${GAP_EXTEND})`,
    bottom: `calc(-1 * ${GAP_EXTEND})`,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: CONNECTOR_TOP,
    right: CONNECTOR_GAP,
    height: '2px',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Bottom-corner connector (└): vertical line from top to connector, then horizontal to the right. */
export const spanLineConnectorEnd = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${GAP_EXTEND})`,
    height: `calc(${CONNECTOR_TOP} + 2px + ${GAP_EXTEND})`,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: CONNECTOR_TOP,
    right: CONNECTOR_GAP,
    height: '2px',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Bridge dimensions for the circuit-style hop over the vertical line. */
const BRIDGE_SIZE = '10px'
const BRIDGE_HALF = '5px'
const BRIDGE_TOP = '6px'
/** Vertical position of the passthrough horizontal line (bridge bottom). */
const BRIDGE_BOTTOM = `calc(${CONNECTOR_TOP} + 1px)`
/** Overlap so horizontal segments tuck under the bridge borders (avoids sub-pixel gaps). */
const BRIDGE_OVERLAP = '1px'

/** Active vertical line with a horizontal pass-through that hops over the vertical line. */
export const spanLineActivePassthrough = style([spanLineColumnBase, {
  // Horizontal line segments with a gap where the bridge arc sits.
  // The gap is slightly narrower than the bridge so segments overlap its borders.
  'backgroundImage': `linear-gradient(to right, var(--span-passthrough-color, var(--border)) 0, var(--span-passthrough-color, var(--border)) calc(50% - ${BRIDGE_HALF} + ${BRIDGE_OVERLAP}), transparent calc(50% - ${BRIDGE_HALF} + ${BRIDGE_OVERLAP}), transparent calc(50% + ${BRIDGE_HALF} - ${BRIDGE_OVERLAP}), var(--span-passthrough-color, var(--border)) calc(50% + ${BRIDGE_HALF} - ${BRIDGE_OVERLAP}), var(--span-passthrough-color, var(--border)) 100%)`,
  'backgroundSize': '100% 2px',
  'backgroundPosition': `0 ${CONNECTOR_TOP}`,
  'backgroundRepeat': 'no-repeat',
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: `calc(-1 * ${GAP_EXTEND})`,
    bottom: `calc(-1 * ${GAP_EXTEND})`,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    boxSizing: 'border-box',
    left: '50%',
    top: BRIDGE_TOP,
    width: BRIDGE_SIZE,
    height: `calc(${BRIDGE_BOTTOM} - ${BRIDGE_TOP} + ${BRIDGE_OVERLAP})`,
    transform: 'translateX(-50%)',
    borderLeft: '2px solid var(--span-passthrough-color, var(--border))',
    borderRight: '2px solid var(--span-passthrough-color, var(--border))',
    borderTop: '2px solid var(--span-passthrough-color, var(--border))',
    borderBottom: 'none',
    borderRadius: `${BRIDGE_HALF} ${BRIDGE_HALF} 0 0`,
  },
}])

/** Empty column with a horizontal pass-through line. */
export const spanLinePassthrough = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: 0,
    right: 0,
    top: CONNECTOR_TOP,
    height: '2px',
    backgroundColor: 'var(--span-passthrough-color, var(--border))',
  },
}])

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
