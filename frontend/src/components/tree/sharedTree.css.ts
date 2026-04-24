import { style } from '@vanilla-extract/css'

export const node = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': '2px var(--space-2)',
  'cursor': 'pointer',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'userSelect': 'none',
  'whiteSpace': 'nowrap',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const nodeSelected = style({
  backgroundColor: 'var(--secondary)',
  selectors: {
    '&:hover': {
      backgroundColor: 'var(--muted)',
    },
  },
})

export const chevron = style({
  'flexShrink': 0,
  'color': 'var(--muted-foreground)',
  'transition': 'transform 150ms cubic-bezier(0.4, 0, 0.2, 1)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const chevronExpanded = style({
  transform: 'rotate(90deg)',
})

export const chevronPlaceholder = style({
  flexShrink: 0,
  width: '16px',
})

export const childrenWrapper = style({
  'display': 'grid',
  'gridTemplateRows': '0fr',
  'visibility': 'hidden',
  'transition': 'grid-template-rows 150ms cubic-bezier(0.4, 0, 0.2, 1), visibility 150ms',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const childrenWrapperExpanded = style({
  gridTemplateRows: '1fr',
  visibility: 'visible',
})

export const childrenInner = style({
  overflow: 'clip',
  minHeight: 0,
})

// Wraps a row's label + diff-stats badge so a single Tooltip target covers
// both. Inner gap matches `node`'s flex gap.
export const labelWithStats = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})
