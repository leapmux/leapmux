import { style } from '@vanilla-extract/css'

export const batchChoice = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  marginBlock: 'var(--space-3)',
})

// Oat clips details content. Keep its intrinsic height inside the flex scroller
// so the summary remains reachable on a short viewport.
export const targetDetails = style({
  flexShrink: 0,
})

export const targetList = style({
  maxHeight: 'min(32vh, 16rem)',
  overflowY: 'auto',
  overflowWrap: 'anywhere',
  marginBlock: 'var(--space-2)',
  paddingInlineStart: 'var(--space-4)',
  display: 'grid',
  gap: 'var(--space-2)',
})

export const target = style({
  display: 'grid',
  gap: 'var(--space-1)',
})
