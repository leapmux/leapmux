import { style } from '@vanilla-extract/css'

// Both labels occupy one cell, so a state change cannot move adjacent controls.
export const labels = style({
  display: 'inline-grid',
  gap: 'inherit',
})

export const label = style({
  gridArea: '1 / 1',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'inherit',
  selectors: {
    '&[aria-hidden="true"]': { visibility: 'hidden' },
  },
})
