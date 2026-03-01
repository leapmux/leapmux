import { globalStyle, style } from '@vanilla-extract/css'

export const popover = style({
  'position': 'fixed',
  'zIndex': 200,
  'display': 'flex',
  'borderRadius': 'var(--radius-small)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--card)',
  'boxShadow': '0 2px 8px rgba(0, 0, 0, 0.15)',
  'opacity': 0.9,
  'transition': 'opacity 0.1s',
  ':hover': {
    opacity: 1,
  },
})

export const quoteButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '4px',
  height: '28px',
  paddingInline: 'var(--space-2)',
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  transition: 'color 0.1s',
  borderRadius: 'var(--radius-small)',
  selectors: {
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

// Separator between adjacent buttons
globalStyle(`${quoteButton} + .${quoteButton}`, {
  borderLeft: '1px solid var(--border)',
})
