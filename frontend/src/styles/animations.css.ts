import { keyframes, style } from '@vanilla-extract/css'

export const spin = keyframes({
  '0%': { transform: 'rotate(0deg)' },
  '100%': { transform: 'rotate(360deg)' },
})

export const spinner = style({
  animation: `${spin} 1s linear infinite`,
})

export const thinkingPulse = keyframes({
  '0%, 100%': { opacity: 0.15 },
  '50%': { opacity: 0.6 },
})
