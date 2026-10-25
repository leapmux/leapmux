import { style } from '@vanilla-extract/css'

/**
 * The schedule-send popover. The trigger reuses `actionButtonClass` from the
 * composer's action row, so it renders as one of Send's siblings. The popover
 * is a `DropdownMenu` card, which supplies the surface, the inset and the
 * viewport clamp. This module supplies the compact layout and keeps field
 * errors inline.
 *
 * The popover element itself takes no `display`: an author `display` beats the
 * user agent's `display: none` for a closed popover, and keeps the closed
 * popover laid out (see `~/styles/popover.css.ts`). The body below carries the
 * column instead.
 */

export const schedulePopover = style({
  minWidth: '260px',
})

export const scheduleBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const scheduleField = style({
  // The body's gap replaces the space after an Oat form field.
  marginBlockEnd: 0,
})

export const scheduleError = style({
  // Oat's field error keeps its typography but does not need an alert card.
  padding: 0,
  border: 'none',
  backgroundColor: 'transparent',
})

export const scheduleActions = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
})
