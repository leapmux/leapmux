import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const appName = style({
  fontSize: '1.25rem',
  fontWeight: 600,
})

export const versionLine = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--font-sm)',
})

export const versionLabel = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--font-xs)',
  opacity: 0.7,
})

export const copyright = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--font-sm)',
})
