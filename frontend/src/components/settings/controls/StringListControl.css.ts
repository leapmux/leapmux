import { style } from '@vanilla-extract/css'

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflow: 'hidden',
})

export const listItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'padding': 'var(--space-2) var(--space-3)',
  'backgroundColor': 'var(--card)',
  'cursor': 'grab',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const dragHandle = style({
  color: 'var(--faint-foreground)',
  cursor: 'grab',
  userSelect: 'none',
})

export const itemName = style({
  flex: 1,
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
})

export const removeButton = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '20px',
  'height': '20px',
  'borderRadius': '3px',
  'color': 'var(--faint-foreground)',
  'cursor': 'pointer',
  'border': 'none',
  'background': 'none',
  'padding': 0,
  ':hover': {
    color: 'var(--danger)',
    backgroundColor: 'var(--card)',
  },
})

export const editWrapper = style({
  flex: 1,
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
})

export const editInput = style({
  'width': '100%',
  'padding': '2px var(--space-1)',
  'fontSize': 'var(--text-7)',
  'fontFamily': 'inherit',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'outline': 'none',
  'boxSizing': 'border-box',
  ':focus': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const listEmpty = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: 'var(--space-2) 0',
  fontStyle: 'italic',
})

export const addRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})
