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

export const menuItem = style({
  width: '100%',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  background: 'none',
  border: 'none',
  borderRadius: 'var(--radius-small)',
  cursor: 'pointer',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  textAlign: 'left',
  selectors: {
    '&:hover, &:focus': {
      backgroundColor: 'var(--accent)',
      outline: 'none',
    },
  },
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
