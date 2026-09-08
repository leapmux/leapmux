import { style } from '@vanilla-extract/css'
import { fieldTrigger } from '~/styles/shared.css'

export const trigger = style([fieldTrigger, {
  width: '100%',
  marginTop: 'var(--space-1)',
  padding: 'var(--space-2) var(--space-3)',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  textAlign: 'left',
}])

export const triggerDisabled = style({
  opacity: 0.5,
  cursor: 'not-allowed',
})

export const triggerValue = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const menu = style({
  margin: 0,
  minWidth: '12rem',
  padding: 'var(--space-1)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-medium)',
})

// Only what is specific to THIS menu. Everything a menu item needs to look like
// one -- the reset, the type scale, the hover fill -- now comes from the shared
// [role="menuitem"] rule in the global stylesheet, and the layout half
// (display, width, padding, cursor, focus outline) from Oat's own rule. This
// block used to restate all of it: a private workaround for the Oat coupling
// that shared rule now owns, and a second copy that could only drift from it.
export const menuItem = style({
  justifyContent: 'space-between',
})

export const menuItemSelected = style({
  backgroundColor: 'var(--accent)',
})

export const menuItemValue = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const check = style({
  color: 'var(--primary)',
  flexShrink: 0,
})
