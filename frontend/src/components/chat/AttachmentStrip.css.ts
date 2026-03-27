import { globalStyle, style } from '@vanilla-extract/css'

export const strip = style({
  display: 'flex',
  gap: 'var(--space-2)',
  overflowX: 'auto',
  scrollbarWidth: 'none',
  padding: 'var(--space-1) var(--space-3) var(--space-1) var(--space-3)',
  flexShrink: 0,
})

globalStyle(`${strip}::-webkit-scrollbar`, {
  display: 'none',
})

export const pill = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: '2px var(--space-2)',
  borderRadius: 'var(--radius-small)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  whiteSpace: 'nowrap',
  maxWidth: '200px',
  flexShrink: 0,
})

export const pillFilename = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const pillIcon = style({
  flexShrink: 0,
  color: 'var(--muted-foreground)',
})

export const removeButton = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'cursor': 'pointer',
  'color': 'var(--muted-foreground)',
  'flexShrink': 0,
  'borderRadius': 'var(--radius-small)',
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--muted)',
  },
})
