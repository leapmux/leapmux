import { style } from '@vanilla-extract/css'

export const wrapper = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const toolbar = style({
  display: 'flex',
  alignItems: 'center',
  marginLeft: 'auto',
  gap: '2px',
  flexShrink: 0,
})

// A flex COLUMN, so the tree inside claims the remaining height through its own
// `flex: 1` -- the same way the dialog's `treeContainer` sizes it. Before this,
// the sidebar sized the tree with `height: 100%` and the dialog with `flex: 1`,
// which forced the tree's own class to carry both rules at once.
export const treeContent = style({
  display: 'flex',
  flexDirection: 'column',
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
  'gap': 'var(--space-1)',
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

/**
 * The region the filter tabs control (role=tabpanel).
 *
 * Purely structural -- the tree and the flat list own their own sizing, so this
 * must not introduce a layout box of its own; `display: contents` keeps the
 * element out of the layout while still carrying the role and the id that
 * aria-controls points at.
 */
export const panel = style({
  display: 'contents',
})
