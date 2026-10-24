import { style } from '@vanilla-extract/css'
import { popoverColumnClamp } from '~/styles/popover.css'

/**
 * The schedule-send popover. The trigger reuses `actionButtonClass` from the
 * composer's action row, so it renders as one of Send's siblings; only the
 * popover body is this module's own.
 */

export const schedulePopover = style([popoverColumnClamp, {
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-large)',
  padding: 'var(--space-2)',
  zIndex: 300,
  minWidth: '260px',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
}])

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
  color: 'var(--destructive-foreground, var(--foreground))',
  fontSize: 'var(--text-8)',
})

export const scheduleActions = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
})
