import { style } from '@vanilla-extract/css'

export const orgList = style({
  maxHeight: '160px',
  overflowY: 'auto',
})

export const orgItem = style({
  all: 'unset',
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  padding: 'var(--space-2) var(--space-3)',
  borderRadius: 'var(--radius-small)',
  color: 'var(--foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'left',
  cursor: 'pointer',
  boxSizing: 'border-box',
  selectors: {
    '&[data-highlighted]': {
      backgroundColor: 'var(--card)',
    },
    '&[data-active]': {
      color: 'var(--primary)',
      fontWeight: 400,
      backgroundColor: 'var(--secondary)',
    },
    '&[data-active][data-highlighted]': {
      backgroundColor: 'var(--muted)',
    },
  },
})

export const personalTag = style({
  fontSize: 'var(--text-8)',
  marginLeft: '4px',
  color: 'var(--faint-foreground)',
})
