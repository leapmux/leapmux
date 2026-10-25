import { style } from '@vanilla-extract/css'

/**
 * The schedule-send popover. The trigger reuses `actionButtonClass` from the
 * composer's action row, so it renders as one of Send's siblings. The popover
 * is a `DropdownMenu` card, which supplies the surface, the inset and the
 * viewport clamp, so this module holds only the body's own layout.
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
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const scheduleInput = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: 'var(--space-1) var(--space-2)',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  backgroundColor: 'var(--background)',
  color: 'var(--foreground)',
  fontSize: 'var(--text-8)',
})

export const scheduleError = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-8)',
})

export const scheduleActions = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
})
