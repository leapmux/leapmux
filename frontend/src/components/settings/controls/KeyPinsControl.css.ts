import { style } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

export const pinRow = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-3)',
  'padding': 'var(--space-2) var(--space-3)',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--card)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-2)',
  'minWidth': 0,
  '@media': {
    // Phone: id on its own row so it keeps the full card width; date +
    // Remove share the second row instead of squeezing the id into a sliver.
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      flexDirection: 'column',
      alignItems: 'stretch',
      gap: 'var(--space-2)',
    },
  },
})

export const pinWorker = style({
  flex: 1,
  minWidth: 0,
})

/** Date + Remove — stays one line beside the id on desktop, under it on phone. */
export const pinMeta = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-3)',
  'flexShrink': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      justifyContent: 'space-between',
    },
  },
})

export const pinDate = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const empty = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  fontStyle: 'italic',
})
