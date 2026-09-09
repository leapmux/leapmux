import { style } from '@vanilla-extract/css'
import { iconSize } from '~/styles/tokens'

export const node = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': '2px var(--space-2)',
  // Every row reserves the height of a row-action button, whether or not it
  // draws one. The row has no height of its own, so the tallest child sets it,
  // and a 24px kebab is taller than the 21px label line box. Without this, a
  // branch group with no branch name, a repo group on an archived workspace and
  // a tab leaf that cannot close all sit 3px shorter than their neighbours, and
  // the sidebar loses its rhythm wherever an action is conditional.
  'minHeight': `calc(${iconSize.container.md} + 4px)`,
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
