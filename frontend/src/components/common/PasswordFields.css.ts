import { style } from '@vanilla-extract/css'

export const strengthRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const strengthProgress = style({
  flex: 1,
})

export const strengthLabel = style({
  fontSize: 'var(--text-8)',
  flexShrink: 0,
})
