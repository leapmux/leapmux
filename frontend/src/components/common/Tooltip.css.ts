import { style } from '@vanilla-extract/css'

export const tooltip = style({
  position: 'fixed',
  margin: 0,
  inset: 'unset',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-8)',
  lineHeight: 1,
  whiteSpace: 'nowrap',
  background: 'var(--card)',
  color: 'var(--foreground)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.15)',
  pointerEvents: 'none',
})
