import { style } from '@vanilla-extract/css'

export const wrapper = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const tabBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: '1px',
  padding: '0 var(--space-2)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  fontSize: 'var(--text-8)',
  backgroundColor: 'inherit',
})

export const tabButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  cursor: 'pointer',
  padding: 'var(--space-1) var(--space-2)',
  color: 'var(--muted-foreground)',
  borderBottom: '2px solid transparent',
  transition: 'color 0.1s, border-color 0.1s',
  whiteSpace: 'nowrap',
  selectors: {
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

export const tabButtonActive = style({
  color: 'var(--foreground)',
  borderBottomColor: 'var(--primary)',
})

export const toolbar = style({
  display: 'flex',
  alignItems: 'center',
  marginLeft: 'auto',
  gap: '2px',
  flexShrink: 0,
})

export const treeContent = style({
  flex: 1,
  overflow: 'hidden',
  minHeight: 0,
})

export const flatList = style({
  flex: 1,
  overflow: 'auto',
  padding: 'var(--space-1) 0',
})

export const flatListItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': '4px',
  'padding': '2px var(--space-2) 2px var(--space-3)',
  'cursor': 'pointer',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'userSelect': 'none',
  'whiteSpace': 'nowrap',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const flatListItemSelected = style({
  backgroundColor: 'var(--secondary)',
  selectors: {
    '&:hover': {
      backgroundColor: 'var(--muted)',
    },
  },
})
