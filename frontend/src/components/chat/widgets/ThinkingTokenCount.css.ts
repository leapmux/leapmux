import { keyframes, style } from '@vanilla-extract/css'
import { motion } from '~/styles/tokens'

// Vertical size of one digit cell — also the per-digit roll distance. Tied to
// the count's font-size (em) so the strip scales with the surrounding text.
const CELL = '1.3em'

// Crossfade duration for a format change. Imported by the component (as
// styles.SWAP_MS) for the fade-out layer's removal timer, so the timer and this
// CSS animation share one source of truth.
export const SWAP_MS = motion.medium

// root is the baseline-aligned item inside the verb row.
export const root = style({
  display: 'inline-block',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  // Fixed-width digits so a rolling column never changes width mid-roll and the
  // hidden ghost matches the visible digits' advance exactly.
  fontVariantNumeric: 'tabular-nums',
  userSelect: 'none',
  whiteSpace: 'nowrap',
  lineHeight: CELL,
})

// Screen-reader-only copy of the full "<n> tokens" text. The visual odometer is
// aria-hidden (its DOM is a pile of 0-9 strips that read as gibberish), so this
// carries the real value for assistive tech — and gives tests a stable hook.
export const srOnly = style({
  position: 'absolute',
  width: '1px',
  height: '1px',
  padding: 0,
  margin: '-1px',
  border: 0,
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
})

// numberBox sizes and baselines the number from an in-flow hidden ghost; the
// rolling digits are painted by absolutely-positioned overlays on top of it, so
// the overlays' clipped (baseline-less) columns never affect where the number
// sits relative to " tokens" and the verb.
export const numberBox = style({
  position: 'relative',
  display: 'inline-block',
})

export const numberGhost = style({
  visibility: 'hidden',
})

const fadeIn = keyframes({ from: { opacity: 0 }, to: { opacity: 1 } })
const fadeOut = keyframes({ from: { opacity: 1 }, to: { opacity: 0 } })

// Easter egg: when the count lands on exactly 777, the number + " tokens" go
// "star power" -- a Mario-star pulse. Two animations layer on the root: a hue
// cycle through the spectrum (the rainbow) and a faster scale+glow throb (the
// pulse). They run on `color`/`transform`, so every glyph -- the rolling digits
// and the unit noun, all of which inherit `color` -- shifts in lockstep.
const starRainbow = keyframes({
  '0%': { color: '#ff3b30' }, // red
  '16%': { color: '#ff9500' }, // orange
  '33%': { color: '#ffd60a' }, // yellow
  '50%': { color: '#34c759' }, // green
  '66%': { color: '#0a84ff' }, // blue
  '83%': { color: '#bf5af2' }, // violet
  '100%': { color: '#ff3b30' }, // back to red for a seamless loop
})
// The throb: scale up a touch and bloom a currentColor glow at the midpoint, so
// the halo takes on whatever rainbow hue is live at that instant.
const starPulse = keyframes({
  '0%': { transform: 'scale(1)', textShadow: 'none' },
  '50%': { transform: 'scale(1.08)', textShadow: '0 0 6px currentColor' },
  '100%': { transform: 'scale(1)', textShadow: 'none' },
})

export const starPower = style({
  'animation': `${starRainbow} 1.4s linear infinite, ${starPulse} 0.7s ease-in-out infinite`,
  // transform-origin at the baseline edge keeps the throb from bobbing the count
  // up and down against the verb it sits beside.
  'transformOrigin': 'center bottom',
  'willChange': 'color, transform',
  '@media': {
    // Honour reduced-motion: drop the animation but keep a static gold so the
    // egg still reads as special without any pulsing.
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      transform: 'none',
      color: '#ffd60a',
    },
  },
})

// Fades a freshly-mounted slot in: the new leading column when the number grows
// a digit, or every slot of the live layer when it is swapped for a unit
// crossfade. Runs once on mount, so persisting (rolling) columns never re-fade.
export const slotEnter = style({
  'animation': `${fadeIn} var(--transition)`,
  '@media': {
    '(prefers-reduced-motion: reduce)': { animation: 'none' },
  },
})

// A layer of rendered digits overlaid on the ghost, and the base both layers
// share. flex-start keeps every column and static char top-aligned in an
// equal-height (CELL) line, so their glyphs line up with each other and with the
// ghost beneath. row-reverse lays the slots out right-to-left so the number is
// anchored at its right edge — a new leading digit is appended (in DOM) and
// appears on the left without reshuffling the existing columns' identities. The
// live layer stays at full opacity; on a format change the prior value is
// overlaid on top (see exitingLayer) and fades out, dissolving to reveal this.
export const liveLayer = style({
  position: 'absolute',
  top: 0,
  left: 0,
  display: 'flex',
  flexDirection: 'row-reverse',
  alignItems: 'flex-start',
})

// A snapshot of the prior value — the live layer's layout plus a fade-out —
// drawn over the live layer during a format change.
export const exitingLayer = style([liveLayer, {
  'animation': `${fadeOut} var(--transition) forwards`,
  '@media': {
    '(prefers-reduced-motion: reduce)': { animation: 'none', opacity: 0 },
  },
}])

// One digit position: a window onto a 0-9 strip, clipped by overflow:hidden.
// Its width and height come from an in-flow hidden sizer digit (see columnSizer)
// rather than the `ch` unit — `ch` does not track the tabular-figure advance, so
// 1ch left visible gaps around each digit and pushed the unit char into
// " tokens" (worse under zoom, where ch and glyph widths round apart). The sizer
// uses the real rendered digit, so columns match the ghost exactly.
//
// position:relative is the containing block for the absolutely-positioned strip.
export const column = style({
  position: 'relative',
  display: 'inline-block',
  overflow: 'hidden',
})

export const columnSizer = style({
  visibility: 'hidden',
})

export const strip = style({
  position: 'absolute',
  top: 0,
  left: 0,
  width: '100%',
  // transform + transition are driven imperatively per column (a forward-rolling
  // odometer that always travels upward, even across a 9->0 carry); only the
  // per-cell metric lives here, read by the inline transform via var(--cell).
  vars: { '--cell': CELL },
})

// One CELL-tall, centered line. Shared by the rolling strip cells and the
// static point/unit chars so both sit on the same row; kept in one place so the
// cell height can't drift between them (a mismatch would misalign the digits
// against the decimal point and k/M unit).
const cellLine = {
  height: CELL,
  lineHeight: CELL,
  textAlign: 'center',
} as const

export const stripCell = style([cellLine, {
  // block (not the span default of inline) so the strip's cells stack vertically
  // at CELL intervals — the whole premise of the translateY roll. As inline text
  // they flow into one horizontal "0123456789" line and the roll slides it out
  // of view for every non-zero digit.
  display: 'block',
}])

// Non-digit characters (the decimal point and the k/M unit), sized like a cell
// so they share the columns' line.
export const staticChar = style([cellLine, {
  display: 'inline-block',
}])
