import { style } from '@vanilla-extract/css'

/**
 * The one right-aligned row of actions: form footers, section actions, and
 * credential-row button groups all render this, so alignment, spacing, and
 * wrap behavior are stated once instead of spelled per surface.
 *
 * Wrapping is on so a row that outgrows a narrow panel stacks instead of
 * overflowing; the gap is space-2, the spacing the dialog footer has always
 * used. The one thing this style deliberately does NOT carry is the dialog
 * footer's block padding -- that is the dialog's chrome, not the actions'
 * convention, and the Dialog stylesheet keeps it.
 */
export const actionsFooter = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
})
