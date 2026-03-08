import { style } from '@vanilla-extract/css'

export const diffStats = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  marginLeft: '4px',
  paddingRight: 'var(--space-1)',
  flexShrink: 0,
  whiteSpace: 'nowrap',
})

export const diffStatsAdded = style({
  color: 'var(--success)',
})

export const diffStatsDeleted = style({
  color: 'var(--danger)',
})
