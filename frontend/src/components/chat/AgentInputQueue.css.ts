import { style } from '@vanilla-extract/css'
import { compactAction, compactActionLabelled, compactActionSize, compactTextSize } from '~/styles/tokens'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  maxHeight: 'min(40vh, 20rem)',
  overflowY: 'auto',
  overscrollBehavior: 'contain',
  // `inputArea` owns the gap to the neighbours. See the `inputArea` comment in
  // `~/components/chat/ChatView.css.ts`.
  padding: 0,
})

/**
 * One queued input: the grip, the text, and the action buttons.
 *
 * FLEX, and not a grid with a fixed track for each of the three.
 *
 * `DragHandle` hides itself under `(any-pointer: fine)`, so the grip renders on
 * a touch-only device alone. A `display: none` grid item leaves its track
 * behind: the text moves into the grip's track, the actions move into the
 * text's `1fr` track, and a preview long enough to reach its max-content width
 * then starves the actions to zero and pushes the buttons out of the row. A
 * `display: none` FLEX item consumes neither a slot nor a gap, so the row keeps
 * its shape whether or not the grip renders.
 */
export const item = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  background: 'var(--card)',
})

/**
 * The row that a fine pointer can drag by its body.
 *
 * The grip is the affordance on touch, and it hides itself on a fine pointer,
 * so the cursor is the only signal a mouse user gets that the row moves. The
 * deleted text glyph carried `cursor: grab` for the same reason.
 */
export const itemDraggable = style({
  '@media': {
    '(any-pointer: fine)': {
      'cursor': 'grab',
      ':active': {
        cursor: 'grabbing',
      },
    },
  },
})

export const body = style({ flex: 1, minWidth: 0 })
export const preview = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  // A literal, unlike `metadata` below, and for the reason `compactTextSize`
  // records: Oat's scale stops at `--text-8` (0.75rem) and `--text-7`
  // (0.875rem), and this line wants a size between the two. One reader, so it
  // needs no name.
  fontSize: '0.82rem',
})
export const metadata = style({
  color: 'var(--muted-foreground)',
  fontSize: compactTextSize,
})
export const error = style({ color: 'var(--danger)', fontSize: compactTextSize })
/**
 * The row's action cluster.
 *
 * It SHRINKS, and that is what makes `flex-wrap` reachable. A flex item's base
 * size is its max-content width, which for a wrap container is the whole row of
 * buttons on one line; `flex-shrink: 0` pins it there, so the shortfall that
 * wrapping needs can never arise and the cluster overflows the row instead --
 * and `root`'s `overflow-y: auto` computes `overflow-x` to `auto`, so the queue
 * scrolls sideways. The BUTTONS keep their own `flex-shrink: 0`
 * (`compactAction` in `~/styles/tokens.ts`), so they wrap at full size rather
 * than squeezing.
 */
export const actions = style({ display: 'flex', flexWrap: 'wrap', gap: 'var(--space-1)', justifyContent: 'flex-end' })

/**
 * One action in a queue row: an outline square holding a single icon.
 *
 * Icon-only, because a row offers up to six actions and the words pushed the
 * row wider than a phone. Each button gives its name to the tooltip and to the
 * accessibility tree instead.
 *
 * This sets the geometry only. Oat declares its button rules inside
 * `@layer base`, and an unlayered class outranks every layered rule whatever
 * the specificity, so the `outline` class still supplies the border and the
 * colours.
 */
export const iconAction = style({
  ...compactAction,
  width: compactActionSize,
  padding: 0,
})

/**
 * Steer, the one action whose icon alone does not say what it does, so it keeps
 * its label at EVERY width.
 *
 * The other five actions give their name to a `<Tooltip>` instead. Steer cannot.
 * A tooltip opens on hover and on focus, and never on touch, so on a phone this
 * word is the only name a sighted user can reach. Hiding it below `sm` took the
 * label away from exactly the surface that needs it most. The word costs about
 * 34px, and a 320px viewport has the room; `actions` wraps if a narrower one
 * does not.
 */
export const steerAction = style({
  ...compactActionLabelled,
  gap: 'var(--space-1)',
})

/** The row that is currently in flight, as the workspace list marks it. */
export const itemDragging = style({
  opacity: 0.4,
})

/**
 * The grip of a row that cannot move: hidden, but still occupying its flex
 * slot so the row's three columns keep their widths.
 *
 * `visibility`, not `display`, for exactly that reason.
 */
export const dragHandleInert = style({
  visibility: 'hidden',
})
