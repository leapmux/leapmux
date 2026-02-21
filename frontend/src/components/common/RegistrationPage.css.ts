import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { authCardXWide, errorText } from '~/styles/shared.css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  backgroundColor: 'var(--background)',
})

export const infoGrid = style({
  display: 'grid',
  gridTemplateColumns: 'auto 1fr',
  gap: `${spacing.xs} ${spacing.md}`,
  marginBottom: spacing.xl,
})

export const infoLabel = style({
  fontWeight: 400,
  color: 'var(--muted-foreground)',
})

export const infoValue = style({
  color: 'var(--foreground)',
})

export const successText = style({
  color: 'var(--foreground)',
})

export const link = style({
  'color': 'var(--primary)',
  'textDecoration': 'none',
  ':hover': {
    textDecoration: 'underline',
  },
})

export const linkSecondary = style({
  'color': 'var(--muted-foreground)',
  'textDecoration': 'none',
  ':hover': {
    textDecoration: 'underline',
  },
})

export const warningBox = style({
  padding: spacing.md,
  marginBottom: spacing.lg,
  backgroundColor: 'var(--lm-warning-subtle)',
  border: '1px solid var(--warning)',
  borderRadius: 'var(--radius-medium)',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  lineHeight: 1.5,
})

export const actionRow = style({
  display: 'flex',
  justifyContent: 'center',
  gap: spacing.lg,
  marginTop: spacing.lg,
})

export const fieldError = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
  fontWeight: 400,
})
