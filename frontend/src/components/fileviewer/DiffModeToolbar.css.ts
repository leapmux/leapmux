import { style } from '@vanilla-extract/css'

export const toolbar = style({
  'position': 'absolute',
  'top': 'var(--space-2)',
  'right': 'var(--space-3)',
  'zIndex': 10,
  'display': 'flex',
  'gap': '1px',
  'borderRadius': 'var(--radius-small)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--card)',
  'opacity': 0.8,
  'transition': 'opacity 0.15s',
  ':hover': {
    opacity: 1,
  },
})

export const toolbarButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '28px',
  paddingInline: 'var(--space-2)',
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  transition: 'color 0.1s, background-color 0.1s',
  whiteSpace: 'nowrap',
  selectors: {
    '&:first-child': {
      borderRadius: 'var(--radius-small) 0 0 var(--radius-small)',
    },
    '&:last-child': {
      borderRadius: '0 var(--radius-small) var(--radius-small) 0',
    },
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

export const toolbarButtonActive = style({
  backgroundColor: 'var(--accent)',
  color: 'var(--foreground)',
})

export const separator = style({
  width: '1px',
  alignSelf: 'stretch',
  backgroundColor: 'var(--border)',
  flexShrink: 0,
})
