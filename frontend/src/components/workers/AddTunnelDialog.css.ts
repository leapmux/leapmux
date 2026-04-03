import { style } from '@vanilla-extract/css'

export { errorText } from '~/styles/shared.css'

export const typeSelector = style({
  display: 'flex',
  gap: 'var(--space-4)',
  marginBottom: 'var(--space-4)',
})

export const typeOption = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const fieldRow = style({
  display: 'grid',
  gridTemplateColumns: '1fr 1fr',
  gap: 'var(--space-3)',
  marginBottom: 'var(--space-3)',
})

export const fieldGroup = style({
  marginBottom: 'var(--space-3)',
})
