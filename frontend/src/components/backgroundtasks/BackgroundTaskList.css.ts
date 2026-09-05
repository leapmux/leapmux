import { keyframes, style } from '@vanilla-extract/css'

export const groupHeader = style({
  display: 'block',
  padding: 'var(--space-1) 0',
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
})

export const taskRow = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: 'var(--space-2)',
  padding: '3px 0',
  fontSize: 'var(--text-7)',
  // Declared, not inherited. A clickable row is a <button>, and Oat's base
  // button rule sets font-weight: var(--font-medium). A static row is a <div>
  // at the normal weight, so without this the two rows render at different
  // weights and an open subagent reads as emphasized. Set on the row, not the
  // title, so the secondary line matches too.
  fontWeight: 'var(--font-normal)',
  lineHeight: 1.4,
  color: 'var(--foreground)',
  width: '100%',
  textAlign: 'left',
  background: 'none',
  border: 'none',
  cursor: 'pointer',
})

export const taskRowStatic = style({
  cursor: 'default',
})

export const taskStruck = style({
  color: 'var(--muted-foreground)',
})

export const taskIcon = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flexShrink: 0,
  width: '18px',
  height: '20px',
})

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

export const taskBody = style({
  flex: 1,
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
})

/**
 * The title line: the title itself, then the status dot at its right end.
 *
 * The dot is a flex sibling of the title, not a float inside it. The title is
 * one clipped line now, so there is no wrapped block for a float to sit above
 * -- a flex row centres the dot on that single line and needs no offset.
 */
export const titleRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})

// Decoration only. `ClippedText` owns the clipping rule -- see
// `~/components/common/ClippedText.tsx`.
//
// `flex: 1` gives the title every pixel the dot does not take, which is what
// puts the ellipsis at the right edge of the row rather than at the end of the
// text. The component supplies the `min-width: 0` that lets it shrink that far.
export const taskTitle = style({
  flex: 1,
})

/**
 * A shell task's title is its COMMAND, so it is set in the monospace face.
 *
 * Same size as any other title: the monospace face alone marks it as code, and
 * stepping it down to the secondary line's size would flatten the
 * title/subtitle hierarchy.
 */
export const taskTitleCommand = style({
  fontFamily: 'var(--font-mono)',
})

// Decoration only. `ClippedText` owns the clipping rule -- see
// `~/components/common/ClippedText.tsx`.
//
// One clipped line, like the title. The activity text a provider reports
// ("Running <what>", a tool name, a token tally) is often longer than the
// sidebar is wide, so the full string is on the tooltip that ClippedText
// attaches -- see the secondary line in `rowBody` in the component.
export const taskSecondary = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

// The box shown in place of the rows when the selected kind has none. Without
// it a kind with no rows renders an empty area that reads as a rendering fault.
//
// `emptyState` is the BOX; the text inside it is the host's `emptyMessage`
// prop, because only the host knows which tab is showing. The two carry
// different names so a reader of BackgroundTaskList never has to decide which
// `emptyMessage` a line means.
export const emptyState = style({
  padding: 'var(--space-4) var(--space-2)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'center',
})

// The same box when the registry could not be LOADED. Carries the danger colour
// because it reports a fault rather than an absence, and the two otherwise read
// identically.
export const emptyStateFailed = style({
  color: 'var(--danger)',
})
