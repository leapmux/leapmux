import { style } from '@vanilla-extract/css'
import { thinkingPulse } from '~/styles/animations.css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: 'var(--space-2) 0',
})

export const dot = style({
  width: '7px',
  height: '7px',
  borderRadius: '50%',
  backgroundColor: 'var(--muted-foreground)',
  opacity: 0.15,
  animation: `${thinkingPulse} 1.2s ease-in-out infinite`,
})
