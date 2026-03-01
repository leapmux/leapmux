import { style } from '@vanilla-extract/css'

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
  gap: 'var(--space-1) var(--space-3)',
  marginBottom: 'var(--space-6)',
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
  padding: 'var(--space-3)',
  marginBottom: 'var(--space-4)',
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
  gap: 'var(--space-4)',
  marginTop: 'var(--space-4)',
})

export const fieldError = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
  fontWeight: 400,
})
