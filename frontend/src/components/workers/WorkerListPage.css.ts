import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { backLink, emptyState, errorText } from '~/styles/shared.css'

export const container = style({
  padding: spacing.xl,
})

export const workerCard = style({
  padding: 'var(--space-3) var(--space-4)',
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  marginTop: spacing.xl,
  marginBottom: spacing.md,
})

export const sectionName = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
})

export const sectionCount = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
})

export const cardGrid = style({
  display: 'flex',
  flexDirection: 'column',
  gap: spacing.sm,
})

export const cardInfo = style({
  display: 'flex',
  flexDirection: 'column',
  gap: spacing.xs,
  minWidth: 0,
  flex: 1,
})

export const cardName = style({
  fontWeight: 600,
  color: 'var(--foreground)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const cardHostname = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const cardMeta = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
})

export const cardRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  flexShrink: 0,
  marginLeft: spacing.md,
})

export const emptySection = style({
  padding: spacing.lg,
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'center',
})
