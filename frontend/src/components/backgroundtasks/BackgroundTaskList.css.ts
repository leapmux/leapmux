import { keyframes, style } from '@vanilla-extract/css'

/**
 * The list root: the kind tab bar, then the rows it filters.
 *
 * The ROWS scroll, not the root, so the tab bar stays put while the rows move
 * under it. Same shape as the Files section, which pairs the same tab bar with
 * a scrolling tree.
 */
export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflow: 'hidden',
})

/**
 * Sidebar variant: fill the section's content box.
 *
 * That box scrolls on its own, and letting it do so would carry the tab bar off
 * the top with the rows. A definite height hands the scrolling to `rows` below
 * instead, and the outer container then never has anything to scroll.
 */
export const sidebarRoot = style({
  height: '100%',
})

/**
 * Popover variant (the ThinkingIndicator's bg-tasks popover).
 *
 * The DropdownMenu card sizes to its content, so capping the list is what caps
 * the card. Both axes need a cap, for different reasons: a long registry
 * overflows the card vertically, and a row holds each of its two lines on one
 * line, so a long shell command asks for the full width of the command.
 *
 * Neither cap restates the VIEWPORT clamp: `popoverCard` in
 * `~/styles/popover.css.ts` already holds the card inside the viewport on both
 * axes, and Oat's global `box-sizing: border-box` means its own padding comes
 * out of that. These two are the tighter, content-shaped limits on top.
 */
export const popoverRoot = style({
  maxHeight: '60vh',
  maxWidth: '360px',
})

/**
 * The scrolling region the kind tabs swap.
 *
 * `overflow-x: hidden` is declared, not left out. `overflow-y: auto` alone makes
 * CSS compute the other axis from `visible` to `auto`, so this box grew a
 * horizontal scrollbar for any descendant that exceeded it. Every row now clips
 * its own text, and this makes that structural: no descendant added later can
 * bring the sideways scroll back.
 */
export const rows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
  overflowY: 'auto',
  overflowX: 'hidden',
  minHeight: 0,
})

// Shown in place of the rows when the selected kind tab has none. Without it a
// tab with no rows renders an empty box that reads as a rendering fault.
export const emptyMessage = style({
  padding: 'var(--space-4) var(--space-2)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'center',
})

// The same box when the registry could not be LOADED. Carries the danger colour
// because it reports a fault rather than an absence, and the two otherwise read
// identically.
export const loadFailedMessage = style({
  color: 'var(--danger)',
})

// Decoration only. `ClippedText` owns the clipping rule -- see
// `~/components/common/ClippedText.tsx`.
//
// `display: block` is defensive, not load-bearing. The header is a <span>, and
// an inline box would drop the vertical padding below; but the header renders
// into `rows`, which is a flex container, and CSS blockifies a flex item
// already. This declaration only holds the padding if `rows` stops being a flex
// container.
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
// attaches -- see renderSecondary in the component.
export const taskSecondary = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})
