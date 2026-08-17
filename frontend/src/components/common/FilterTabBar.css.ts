import { style } from '@vanilla-extract/css'

// A row of filter tabs above the region they swap. Sized for a sidebar section
// header and for a popover card alike: the type is the small one, the padding is
// horizontal only, and the rule under it is the boundary against the panel.
export const tabBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: '1px',
  padding: '0 var(--space-2)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  fontSize: 'var(--text-8)',
  backgroundColor: 'inherit',
  // Tabs are nowrap; a long set (the file filters in a narrow sidebar) must
  // scroll inside the bar instead of widening the parent.
  overflowX: 'auto',
  minWidth: 0,
  width: '100%',
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
    // `all: unset` above drops the UA focus ring with everything else, and this
    // is a roving tabindex: selection MOVES focus between the tabs, so without a
    // ring the keyboard user cannot see where they are.
    '&:focus-visible': {
      outline: '2px solid var(--ring)',
      outlineOffset: '-2px',
    },
  },
})

export const tabButtonActive = style({
  color: 'var(--foreground)',
  borderBottomColor: 'var(--primary)',
})
