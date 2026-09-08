import { style } from '@vanilla-extract/css'

export const trigger = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  // Never shrinks: the input beside it takes the slack instead, so a long path
  // cannot push the drive off the row.
  flexShrink: 0,
  padding: 'var(--space-1) var(--space-2)',
  fontSize: 'var(--text-7)',
  lineHeight: 'var(--leading-normal)',
  backgroundColor: 'var(--background)',
  color: 'var(--foreground)',
  border: '1px solid var(--input)',
  borderRadius: 'var(--radius-medium)',
  transition: 'border-color var(--transition-fast), box-shadow var(--transition-fast)',
  selectors: {
    '&:focus-visible': {
      outline: 'none',
      borderColor: 'var(--ring)',
      boxShadow: '0 0 0 2px rgb(from var(--ring) r g b / 0.2)',
    },
  },
})

export const triggerIcon = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})

export const triggerValue = style({
  whiteSpace: 'nowrap',
})

export const triggerChevron = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})

export const menu = style({
  margin: 0,
  minWidth: '10rem',
  padding: 'var(--space-1)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})
