import { style } from '@vanilla-extract/css'
import { thinkingPulse } from '~/styles/animations.css'
import { spacing } from '~/styles/tokens'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: `${spacing.sm} ${spacing.lg}`,
})

export const dot = style({
  width: '7px',
  height: '7px',
  borderRadius: '50%',
  backgroundColor: 'var(--muted-foreground)',
  opacity: 0.15,
  animation: `${thinkingPulse} 1.2s ease-in-out infinite`,
})
