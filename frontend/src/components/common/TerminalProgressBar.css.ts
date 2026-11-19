import { style } from '@vanilla-extract/css'

/** Thin task-progress bar for the OSC 9;4 progress of a terminal tab. */
export const bar = style({
  'width': '24px',
  'height': '3px',
  'borderRadius': '2px',
  'backgroundColor': 'var(--border)',
  'overflow': 'hidden',
  'flexShrink': 0,
  '::after': {
    content: '""',
    display: 'block',
    height: '100%',
    backgroundColor: 'var(--primary)',
    width: 'var(--progress-percent, 0%)',
  },
})
