import { style } from '@vanilla-extract/css'

export const trigger = style({
  width: '100%',
  marginTop: 'var(--space-1)',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  lineHeight: 'var(--leading-normal)',
  backgroundColor: 'var(--background)',
  color: 'var(--foreground)',
  border: '1px solid var(--input)',
  borderRadius: 'var(--radius-medium)',
  transition: 'border-color var(--transition-fast), box-shadow var(--transition-fast)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  textAlign: 'left',
  selectors: {
    '&:focus': {
      outline: 'none',
      borderColor: 'var(--ring)',
      boxShadow: '0 0 0 2px rgb(from var(--ring) r g b / 0.2)',
    },
  },
})

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

export const triggerLabel = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const triggerChevron = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
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
