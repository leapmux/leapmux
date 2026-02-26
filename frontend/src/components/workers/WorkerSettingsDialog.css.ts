import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { errorText } from '~/styles/shared.css'

export const description = style({
  color: 'var(--muted-foreground)',
  lineHeight: 1.5,
  marginBottom: spacing.md,
})

export const warning = style({
  padding: spacing.md,
  backgroundColor: 'var(--lm-danger-subtle)',
  border: '1px solid var(--danger)',
  borderRadius: 'var(--radius-medium)',
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
  lineHeight: 1.5,
  marginBottom: spacing.lg,
})
