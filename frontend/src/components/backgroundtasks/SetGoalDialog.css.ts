import { style } from '@vanilla-extract/css'

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: '320px',
})

export const label = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const input = style({
  width: '100%',
  fontFamily: 'inherit',
  fontSize: 'var(--text-7)',
  resize: 'vertical',
})

export const actions = style({
  display: 'flex',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
})

export const button = style({
  fontSize: 'var(--text-7)',
  padding: 'var(--space-1) var(--space-3)',
  borderRadius: '4px',
  border: '1px solid var(--border-color)',
  background: 'transparent',
  color: 'var(--foreground)',
  cursor: 'pointer',
  selectors: {
    '&:hover:not(:disabled)': { background: 'var(--subtle-background)' },
    '&:disabled': { opacity: 0.5, cursor: 'default' },
  },
})
