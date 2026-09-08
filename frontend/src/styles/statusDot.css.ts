import { keyframes, style } from '@vanilla-extract/css'

/**
 * The status-dot palette, shared by every surface that reports a run's state
 * with a coloured dot.
 *
 * Here rather than beside one of its consumers: the background-task rows and
 * the session-goal card both draw it, and they no longer live in one directory.
 * The dot's SHAPE is `~/styles/shared.css.ts`, which the workers section uses
 * as well; this file holds only the palette.
 */
// A slow breath, so an in-progress row is identifiable at a glance without the
// column becoming busy. Bottoms out well above zero: a dot that disappears
// reads as a rendering fault rather than as activity.
const dotPulse = keyframes({
  '0%, 100%': { opacity: 1 },
  '50%': { opacity: 0.35 },
})

// Status is carried by the dot's COLOR, so every row keeps the same shape and
// the column reads as a status light rather than a set of glyphs to learn. The
// shape itself is the shared dot in `~/styles/shared.css.ts`, which the workers
// section uses as well; only this palette is specific to the section.
export const statusDotActive = style({
  'background': 'var(--primary)',
  '@media': {
    // Motion is the RUNNING signal, so it is opt-out, not opt-in. It cannot be
    // the only signal, though: a reader who suppresses motion sees a static dot,
    // which is why queued carries its own shape below rather than sharing this
    // colour.
    '(prefers-reduced-motion: no-preference)': {
      animation: `${dotPulse} 2s ease-in-out infinite`,
    },
  },
})
// Queued: a hollow ring in the same colour as running. The distinction survives
// with motion suppressed, where the pulse above does not.
export const statusDotPending = style({
  background: 'transparent',
  boxShadow: 'inset 0 0 0 1.5px var(--primary)',
})
export const statusDotSuccess = style({ background: 'var(--success)' })
export const statusDotDanger = style({ background: 'var(--danger)' })
// A user's explicit stop is neither a success nor a failure.
export const statusDotMuted = style({ background: 'var(--muted-foreground)' })
