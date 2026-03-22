import { style, styleVariants } from '@vanilla-extract/css'

/** Container for all thread line columns. */
export const threadLinesContainer = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'stretch',
  flexShrink: 0,
})

/** Base style for a single thread line column. */
const threadLineColumnBase = style({
  width: '20px',
  position: 'relative',
  flexShrink: 0,
})

/** Vertical line running through the column center. */
export const threadLineActive = style([threadLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: 0,
    bottom: 0,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--thread-line-color, var(--border))',
  },
}])

/** Empty column (no active scope). */
export const threadLineEmpty = style([threadLineColumnBase])

/** Horizontal connector from the vertical line to the message. */
export const threadLineConnector = style([threadLineColumnBase, {
  '::before': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: 0,
    bottom: 0,
    width: '2px',
    transform: 'translateX(-50%)',
    backgroundColor: 'var(--thread-line-color, var(--border))',
  },
  '::after': {
    content: '""',
    position: 'absolute',
    left: '50%',
    top: '50%',
    right: 0,
    height: '2px',
    backgroundColor: 'var(--thread-line-color, var(--border))',
  },
}])

/** Thread line color palette (cycled by color index). */
const PALETTE = [
  'var(--color-blue-9)',
  'var(--color-green-9)',
  'var(--color-orange-9)',
  'var(--color-purple-9)',
  'var(--color-pink-9)',
  'var(--color-teal-9)',
  'var(--color-yellow-9)',
  'var(--color-red-9)',
]

export const threadLineColors = styleVariants(
  Object.fromEntries(PALETTE.map((color, i) => [`color${i}`, { vars: { '--thread-line-color': color } }])),
)

export const PALETTE_SIZE = PALETTE.length
