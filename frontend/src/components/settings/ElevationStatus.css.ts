import { style } from '@vanilla-extract/css'

/**
 * The verified-session line inside its alert box.
 *
 * The box itself is `~/components/common/Alert`, styled entirely by
 * @knadh/oat's `[role="alert"]` rules -- the same box the panel's "Changes in
 * this group apply after a hub restart." warning draws.
 *
 * That box is itself a FLEX ROW, so a child of it is a flex item and shrinks
 * to its own content. `width: 100%` plus `flex: 1` is what makes this fill the
 * alert; without them the layout below aligns against the text instead of
 * against the box, and "End now" sits beside the sentence rather than at the
 * right edge.
 */
export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  width: '100%',
  flex: 1,
  minWidth: 0,
})

/** The sentence and the control that ends the window, on one wrapping line. */
export const row = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  flexWrap: 'wrap',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const text = style({
  minWidth: 0,
})
