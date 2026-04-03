import { style } from '@vanilla-extract/css'

export const errorText = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})

export const successText = style({
  color: 'var(--success)',
  fontSize: 'var(--text-7)',
})

export const warningText = style({
  color: 'var(--warning)',
  fontSize: 'var(--text-7)',
})

export const emptyState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
})

// Menu utilities

export const dangerMenuItem = style({
  color: 'var(--danger)',
})

export const menuSectionHeader = style({
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  padding: 'var(--space-1) var(--space-3)',
})

// Layout utilities

export const inlineFlex = style({
  display: 'inline-flex',
})

export const centeredFull = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
})

export const heightFull = style({
  height: '100%',
})

// Card width variants

export const cardNarrow = style({
  width: '360px',
})

export const cardMedium = style({
  width: '400px',
})

export const cardWide = style({
  width: '440px',
})
