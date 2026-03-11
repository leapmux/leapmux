import { style } from '@vanilla-extract/css'

export const wrapper = style({
  display: 'grid',
  gridTemplateRows: '0fr',
  opacity: 0,
  marginLeft: 'calc(-1 * var(--space-1))',
})

export const wrapperInner = style({
  overflow: 'hidden',
})

export const container = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-2) 0',
  color: 'var(--primary)',
})

export const compass = style({
  width: '24px',
  height: '24px',
  flexShrink: 0,
})

export const verb = style({
  fontSize: 'var(--text-7)',
  userSelect: 'none',
})
