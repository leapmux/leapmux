import { style } from '@vanilla-extract/css'

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const description = style({
  margin: 0,
})

export const steps = style({
  margin: 0,
  paddingInlineStart: 'var(--space-5)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const note = style({
  margin: 0,
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})
