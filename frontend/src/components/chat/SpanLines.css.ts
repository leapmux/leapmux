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

/** Vertical line running through the column center. */
export const spanLineActive = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: 0,
    bottom: 0,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Empty column (no active span). */
export const spanLineEmpty = style([spanLineColumnBase])

/** Horizontal connector from the vertical line to the message. */
export const spanLineConnector = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: 0,
    bottom: 0,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: '50%',
    right: 0,
    height: '2px',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
}])

/** Active vertical line with a horizontal pass-through from another span's connector. */
export const spanLineActivePassthrough = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: 0,
    bottom: 0,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--span-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: 0,
    right: 0,
    top: '50%',
    height: '2px',
    backgroundColor: 'var(--span-passthrough-color, var(--border))',
  },
}])

/** Empty column with a horizontal pass-through line. */
export const spanLinePassthrough = style([spanLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: 0,
    right: 0,
    top: '50%',
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
