import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export const container = style({
  position: 'relative',
  borderTop: '1px solid var(--border)',
  padding: spacing.sm,
  flexShrink: 0,
})

export const trigger = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'width': '100%',
  'padding': `${spacing.sm} ${spacing.md}`,
  'borderRadius': 'var(--radius-medium)',
  'color': 'var(--foreground)',
  'fontSize': 'var(--text-7)',
  'fontWeight': 400,
  'cursor': 'pointer',
  'textAlign': 'left',
  'boxSizing': 'border-box',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const orgList = style({
  maxHeight: '160px',
  overflowY: 'auto',
})

export const orgItem = style({
  all: 'unset',
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  padding: `${spacing.sm} ${spacing.md}`,
  borderRadius: 'var(--radius-small)',
  color: 'var(--foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'left',
  cursor: 'pointer',
  boxSizing: 'border-box',
  selectors: {
    '&[data-highlighted]': {
      backgroundColor: 'var(--card)',
    },
  },
})

export const orgItemActive = style({
  all: 'unset',
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  padding: `${spacing.sm} ${spacing.md}`,
  borderRadius: 'var(--radius-small)',
  color: 'var(--primary)',
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  textAlign: 'left',
  cursor: 'pointer',
  boxSizing: 'border-box',
  backgroundColor: 'var(--secondary)',
  selectors: {
    '&[data-highlighted]': {
      backgroundColor: 'var(--muted)',
    },
  },
})

export const personalTag = style({
  fontSize: 'var(--text-8)',
  marginLeft: '4px',
  color: 'var(--faint-foreground)',
})
