import { keyframes, style } from '@vanilla-extract/css'

// The checkbox occupies a 1rem square.
// SVG draws each outline and fill inside the same rectangle.
export const svg = style({
  display: 'inline-block',
  width: '1rem',
  height: '1rem',
  flexShrink: 0,
})

// The pending outline uses a 1.5-unit stroke in the 24-unit viewBox.
// At a 1rem display size, this matches Oat's 1px checkbox border.
export const boxPending = style({
  fill: 'var(--background)',
  stroke: 'var(--input)',
  strokeWidth: 1.5,
})

// Fill the whole square for completed and deleted tasks.
// A contrasting glyph identifies each state.
export const boxCompleted = style({
  fill: 'var(--primary)',
})

export const boxDeleted = style({
  fill: 'var(--danger)',
})

export const glyph = style({
  fill: 'none',
  strokeWidth: 4,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
})

export const glyphCompleted = style({
  stroke: 'var(--primary-foreground)',
})

export const glyphBlocked = style({
  stroke: 'var(--muted-foreground)',
})

export const glyphDeleted = style({
  stroke: 'var(--danger-foreground)',
})

// Each animation cycle moves by one complete dash pattern: 6 + 4 = 10.
// The 1.4-second period gives the progress outline a steady speed.
const ants = keyframes({
  to: { strokeDashoffset: '-10' },
})

// The progress outline uses the same rectangle as the pending outline.
// It hides the static border while its dashes move.
export const antsRect = style({
  'fill': 'none',
  'stroke': 'var(--primary)',
  'strokeWidth': 1.5,
  'strokeDasharray': '6 4',
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      animation: `${ants} 1.4s linear infinite`,
    },
  },
})
