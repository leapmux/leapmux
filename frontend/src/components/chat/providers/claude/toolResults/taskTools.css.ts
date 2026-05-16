import { style } from '@vanilla-extract/css'

export const detailList = style({
  margin: 'var(--space-2) 0 0 var(--space-6)',
  listStyle: 'none',
  padding: 0,
})

export const detailLine = style({
  color: 'var(--color-muted-foreground)',
  fontSize: 'var(--font-size-sm)',
})

export const idRef = style({
  marginRight: 'var(--space-1)',
})
