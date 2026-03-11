import { keyframes, style } from '@vanilla-extract/css'

const spinOnce = keyframes({
  '0%': { transform: 'rotate(0deg)' },
  '100%': { transform: 'rotate(360deg)' },
})

export const spinning = style({
  animation: `${spinOnce} 0.4s ease-in-out`,
})
