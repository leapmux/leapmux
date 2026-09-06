import { style } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

/**
 * The edge of a square action button in a queue row. Not on the spacing
 * scale: it is a control size, and `steerAction` matches it so a row of
 * buttons keeps one height whether or not Steer shows its label.
 *
 * Exported because the pause banner's Resume button sits in the same column
 * and has to be the same height. One value, so the two cannot drift.
 */
export const ACTION_SIZE = '1.75rem'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  maxHeight: 'min(40vh, 20rem)',
  overflowY: 'auto',
  overscrollBehavior: 'contain',
  // `inputArea` owns the gap to the neighbours; see its comment.
  padding: 0,
})

export const item = style({
  display: 'grid',
  gridTemplateColumns: 'auto minmax(0, 1fr) auto',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  background: 'var(--card)',
})

export const body = style({ minWidth: 0 })
export const preview = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  fontSize: '0.82rem',
})
export const metadata = style({
  color: 'var(--muted-foreground)',
  fontSize: '0.72rem',
})
export const error = style({ color: 'var(--danger)', fontSize: '0.72rem' })
export const actions = style({ display: 'flex', flexWrap: 'wrap', gap: 'var(--space-1)', justifyContent: 'flex-end' })
/**
 * One action in a queue row: an outline square holding a single icon.
 *
 * Icon-only, because a row offers up to six actions and the words pushed the
 * row wider than a phone. Each button states its name to the tooltip and to
 * the accessibility tree instead.
 *
 * This sets the geometry only. Oat declares its button rules inside
 * `@layer base`, and an unlayered class outranks every layered rule whatever
 * the specificity, so the `outline` class still supplies the border and the
 * colours.
 */
export const iconAction = style({
  width: ACTION_SIZE,
  height: ACTION_SIZE,
  padding: 0,
  flexShrink: 0,
})

/**
 * Steer, the one action whose icon alone does not say what it does, so it
 * keeps its label on a wide viewport. Below `sm` the label hides and the
 * button becomes the same square as the others.
 */
export const steerAction = style({
  'height': ACTION_SIZE,
  'padding': '0 var(--space-2)',
  'gap': 'var(--space-1)',
  'fontSize': '0.72rem',
  'flexShrink': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      width: ACTION_SIZE,
      padding: 0,
    },
  },
})

export const steerLabel = style({
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      display: 'none',
    },
  },
})

/** The row that is currently in flight, as the workspace list marks it. */
export const itemDragging = style({
  opacity: 0.4,
})

/**
 * The grip of a row that cannot move: hidden, but still occupying its grid
 * cell so the row's three columns do not shift.
 *
 * `visibility`, not `display`, for exactly that reason.
 */
export const dragHandleInert = style({
  visibility: 'hidden',
})
