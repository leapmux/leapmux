import { style } from '@vanilla-extract/css'

export { backLink, emptyState, errorText } from '~/styles/shared.css'

export const container = style({
  padding: 'var(--space-6)',
})

export const workerCard = style({
  padding: 'var(--space-3) var(--space-4)',
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  marginTop: 'var(--space-6)',
  marginBottom: 'var(--space-3)',
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
  gap: 'var(--space-2)',
})

export const cardInfo = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
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
  gap: 'var(--space-2)',
  flexShrink: 0,
  marginLeft: 'var(--space-3)',
})

export const emptySection = style({
  padding: 'var(--space-4)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'center',
})
