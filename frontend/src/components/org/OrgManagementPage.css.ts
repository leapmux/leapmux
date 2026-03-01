import { style } from '@vanilla-extract/css'

export { backLink, emptyState, errorText, successText } from '~/styles/shared.css'

export const container = style({
  padding: 'var(--space-6)',
  maxWidth: '800px',
})

export const infoRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-3)',
  marginBottom: 'var(--space-3)',
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
  gap: 'var(--space-2)',
  marginBottom: 'var(--space-4)',
})

export const deleteSection = style({
  marginBottom: 'var(--space-8)',
  padding: 'var(--space-6)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--danger)',
  borderRadius: 'var(--radius-medium)',
})

export const deleteDescription = style({
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-4)',
})
