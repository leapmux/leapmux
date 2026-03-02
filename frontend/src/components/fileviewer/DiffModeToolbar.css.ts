import { style } from '@vanilla-extract/css'

// Wrapper positioned absolutely to center the toolbar without affecting layout.
export const toolbarWrapper = style({
  position: 'absolute',
  top: 'var(--space-2)',
  left: 0,
  right: 0,
  display: 'flex',
  justifyContent: 'center',
  zIndex: 10,
  pointerEvents: 'none',
})

export const toolbar = style({
  'display': 'flex',
  'gap': '1px',
  'borderRadius': 'var(--radius-small)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--card)',
  'opacity': 0.8,
  'transition': 'opacity 0.15s',
  'pointerEvents': 'auto',
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
