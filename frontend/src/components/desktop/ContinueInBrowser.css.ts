import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '100vh',
  gap: 'var(--space-4)',
  padding: 'var(--space-6)',
  textAlign: 'center',
})

export const title = style({
  fontSize: '1.5rem',
  fontWeight: 600,
  margin: 0,
})

export const description = style({
  color: 'var(--color-text-secondary)',
  margin: 0,
})

export const actions = style({
  display: 'flex',
  gap: 'var(--space-3)',
})

export const error = style({
  color: 'var(--color-danger)',
  margin: 0,
})

export const backLink = style({
  background: 'none',
  border: 'none',
  color: 'var(--color-text-secondary)',
  cursor: 'pointer',
  textDecoration: 'underline',
})
