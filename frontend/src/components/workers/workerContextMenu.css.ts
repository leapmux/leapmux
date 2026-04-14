import { style } from '@vanilla-extract/css'

export const infoButton = style({
  textAlign: 'left',
  lineHeight: '1.35',
})

export const infoGrid = style({
  display: 'grid',
  gridTemplateColumns: 'max-content 1fr',
  gap: '0 var(--space-2)',
  alignItems: 'start',
})
