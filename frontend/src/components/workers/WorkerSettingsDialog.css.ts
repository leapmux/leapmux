import { style } from '@vanilla-extract/css'

export { errorText } from '~/styles/shared.css'

export const description = style({
  color: 'var(--muted-foreground)',
  lineHeight: 1.5,
  marginBottom: 'var(--space-3)',
})

export const warning = style({
  padding: 'var(--space-3)',
  backgroundColor: 'var(--lm-danger-subtle)',
  border: '1px solid var(--danger)',
  borderRadius: 'var(--radius-medium)',
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
  lineHeight: 1.5,
  marginBottom: 'var(--space-4)',
})
