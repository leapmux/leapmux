import { globalStyle, style } from '@vanilla-extract/css'

export const todoList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
})

// `center`, because the label is one clipped line: it centres against the
// checkbox rather than sitting at the top of the taller icon box. The wrapping
// variant below puts it back to `flex-start`.
export const todoItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: '3px 0',
  fontSize: 'var(--text-7)',
  lineHeight: 1.4,
  color: 'var(--foreground)',
})

export const todoStruck = style({
  color: 'var(--muted-foreground)',
  textDecoration: 'line-through',
})

export const todoInProgress = style({
  color: 'var(--primary)',
})

export const todoIcon = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flexShrink: 0,
  width: '18px',
  height: '20px',
})

// Decoration only. `ClippedText` owns the clipping rule and supplies the
// `min-width: 0` that lets this `flex: 1` item shrink past its own text -- see
// `~/components/common/ClippedText.tsx`, which is the sole owner so that
// removing the rule from either place stays visible.
export const todoText = style({
  flex: 1,
})

/**
 * The WRAPPING variant, for a surface with room to show the whole label.
 *
 * The compact rule above suits the sidebar section (~208px) and the
 * ThinkingIndicator popover, which the card width caps. It does not suit the
 * chat transcript: a tool card stretches to the tile, so a to-do that used to
 * wrap and read in full was clipped to one line for no gain.
 *
 * These two rules are the ONE authorized override of the clipping that
 * `ClippedText` otherwise owns. They win on specificity -- (0,2,0) against the
 * shared rule's (0,1,0) -- and not on the order the bundler emits the two
 * stylesheets, which is what makes a cross-stylesheet override fragile.
 */
export const todoListWrapping = style({})

globalStyle(`${todoListWrapping} ${todoText}`, {
  whiteSpace: 'normal',
  overflow: 'visible',
  textOverflow: 'clip',
  // `anywhere`, not `break-word`: a to-do label can hold a path or an
  // identifier that no space breaks.
  overflowWrap: 'anywhere',
})

// A wrapped label is taller than the checkbox, so the checkbox sits against the
// FIRST line rather than the middle of the block.
globalStyle(`${todoListWrapping} ${todoItem}`, {
  alignItems: 'flex-start',
})

// Puts the 1rem checkbox down a sub-pixel so it sits on the first line's
// baseline instead of floating above it. Needed only under `flex-start`.
globalStyle(`${todoListWrapping} ${todoIcon}`, {
  marginTop: '0.25px',
})
