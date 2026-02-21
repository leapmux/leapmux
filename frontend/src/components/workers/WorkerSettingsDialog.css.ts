import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { errorText } from '~/styles/shared.css'

export const memberList = style({
  maxHeight: '200px',
  overflowY: 'auto',
  display: 'flex',
  flexDirection: 'column',
  gap: spacing.xs,
  marginBottom: spacing.lg,
  padding: spacing.sm,
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const memberItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': spacing.sm,
  'padding': `${spacing.xs} ${spacing.sm}`,
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

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
