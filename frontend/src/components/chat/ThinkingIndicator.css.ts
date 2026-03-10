import { keyframes, style } from '@vanilla-extract/css'

const fadeIn = keyframes({
  from: { opacity: 0 },
  to: { opacity: 1 },
})

export const container = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: 'var(--space-2) 0',
  color: 'var(--primary)',
  animation: `${fadeIn} 0.3s ease-out`,
  transition: 'opacity 0.3s ease-out',
})

export const compass = style({
  width: '20px',
  height: '20px',
  flexShrink: 0,
})

export const verb = style({
  fontSize: 'var(--text-7)',
  userSelect: 'none',
})
