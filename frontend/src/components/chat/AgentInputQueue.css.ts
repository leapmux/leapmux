import { style } from '@vanilla-extract/css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '0.35rem',
  padding: '0.5rem 0.65rem 0',
})

export const item = style({
  display: 'grid',
  gridTemplateColumns: 'auto minmax(0, 1fr) auto',
  alignItems: 'center',
  gap: '0.5rem',
  padding: '0.45rem 0.55rem',
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
export const actions = style({ display: 'flex', flexWrap: 'wrap', gap: '0.25rem', justifyContent: 'flex-end' })
export const action = style({ fontSize: '0.72rem', padding: '0.15rem 0.35rem' })
export const drag = style({ cursor: 'grab', color: 'var(--muted-foreground)', userSelect: 'none' })
