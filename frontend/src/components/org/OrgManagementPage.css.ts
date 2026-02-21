import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { backLink, emptyState, errorText, successText } from '~/styles/shared.css'

export const container = style({
  padding: spacing.xl,
  maxWidth: '800px',
})

export const infoRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.md,
  marginBottom: spacing.md,
})

export const infoLabel = style({
  fontWeight: 400,
  color: 'var(--muted-foreground)',
  minWidth: '100px',
})

export const infoValue = style({
  color: 'var(--foreground)',
})

export const inviteForm = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  marginBottom: spacing.lg,
})

export const deleteSection = style({
  marginBottom: spacing.xxl,
  padding: spacing.xl,
  backgroundColor: 'var(--card)',
  border: '1px solid var(--danger)',
  borderRadius: 'var(--radius-medium)',
})

export const deleteDescription = style({
  color: 'var(--muted-foreground)',
  marginBottom: spacing.lg,
})
