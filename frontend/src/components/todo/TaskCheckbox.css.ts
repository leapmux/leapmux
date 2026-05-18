import { keyframes, style } from '@vanilla-extract/css'

// The checkbox is a 1rem × 1rem SVG square. The outline + fill are
// drawn inside the SVG (no CSS border) so every state — including the
// in_progress marching-ants — occupies the same visual footprint.
export const svg = style({
  display: 'inline-block',
  width: '1rem',
  height: '1rem',
  flexShrink: 0,
})

// Static (un-animated) box outline used for the pending state. Stroke
// width 1.5 in a 24-unit viewBox → 1px on screen at 1rem display,
// matching Oat's native `input[type=checkbox]` border.
export const boxPending = style({
  fill: 'var(--background)',
  stroke: 'var(--input)',
  strokeWidth: 1.5,
})

// Filled box for terminal states (completed / deleted). No stroke;
// the entire box is colored and a contrast-colored glyph overlays it.
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

export const glyphDeleted = style({
  stroke: 'var(--danger-foreground)',
})

// One full dash-cycle per animation loop (dash 6 + gap 4 = 10) so
// the loop is seamless. Slower than the original 0.8s so the motion
// reads as steady progress rather than urgent chasing.
const ants = keyframes({
  to: { strokeDashoffset: '-10' },
})

// Marching-ants box used for the in_progress state. Same x/y/width/
// height/rx as the pending box so the visible square is the same size
// — no gray border peeks through behind it.
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
