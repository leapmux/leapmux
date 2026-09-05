import { style } from '@vanilla-extract/css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  maxHeight: 'min(40vh, 20rem)',
  overflowY: 'auto',
  overscrollBehavior: 'contain',
  padding: 'var(--space-2) var(--space-3) 0',
})

export const item = style({
  display: 'grid',
  gridTemplateColumns: 'auto minmax(0, 1fr) auto',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2)',
  border: '1px solid var(--border)',
  borderRadius: '0.45rem',
  background: 'var(--card)',
})

export const body = style({ minWidth: 0 })
export const preview = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  fontSize: '0.82rem',
})
export const metadata = style({
  color: 'var(--muted-foreground)',
  fontSize: '0.72rem',
})
export const error = style({ color: 'var(--danger)', fontSize: '0.72rem' })
export const actions = style({ display: 'flex', flexWrap: 'wrap', gap: 'var(--space-1)', justifyContent: 'flex-end' })
export const action = style({ fontSize: '0.72rem', padding: 'var(--space-1) var(--space-2)' })
export const drag = style({ cursor: 'grab', color: 'var(--muted-foreground)', userSelect: 'none' })
