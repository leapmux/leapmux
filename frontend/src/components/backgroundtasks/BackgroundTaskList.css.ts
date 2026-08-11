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
 * overflowed the card vertically, and one long shell command -- the rows wrap,
 * but the flex column still sizes to the widest unbroken run -- stretched it
 * sideways.
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

/** The scrolling region the kind tabs swap. */
export const rows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
  overflowY: 'auto',
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

export const groupHeader = style({
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

/**
 * The status dot, FLOATED right inside the title.
 *
 * Float, not a flex sibling: the dot belongs at the right end of the title's
 * FIRST line, and a flex row would centre it against the whole wrapped block
 * instead. A right float sits at the top-right of its container, and because
 * the dot is only 8px tall in a ~17px line box, it shortens exactly that first
 * line -- every line below it, and the secondary line in the block underneath,
 * run the full width.
 *
 * 8px matches the workers section's dot (~/components/workers/workerSection.css.ts), so the two
 * sidebar sections read as one vocabulary.
 */
export const statusDot = style({
  float: 'right',
  width: 8,
  height: 8,
  borderRadius: '50%',
  flexShrink: 0,
  marginLeft: 'var(--space-2)',
  // Centres the dot on the first line's box. In em so it tracks the font size
  // rather than needing a second magic number per variant.
  marginTop: '0.35em',
})

// Status is carried by the dot's COLOR, so every row keeps the same shape and
// the column reads as a status light rather than a set of glyphs to learn.
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

export const taskTitle = style({
  // break-word + hyphens, NOT `anywhere`: the browser hyphenates at real
  // syllable boundaries (the document is lang="en"), and only falls back to a
  // hard mid-word break for a token it cannot hyphenate. `anywhere` would win
  // every time and hyphenation would never run.
  overflowWrap: 'break-word',
  hyphens: 'auto',
})

/**
 * A shell task's title is its COMMAND, so it is set in the monospace face and
 * wrapped like code, not like prose: hyphenation off (a hyphen inserted into a
 * path or a flag reads as part of the command), and breaking allowed anywhere
 * so a long `/path/like/this` wraps at the edge instead of pushing the whole
 * token to the next line and leaving the first one half empty.
 */
export const taskTitleCommand = style({
  fontFamily: 'var(--font-mono)',
  // Same size as any other title: the monospace face alone marks it as code,
  // and stepping it down to the secondary line's size would flatten the
  // title/subtitle hierarchy. `anywhere` is what actually buys the width back
  // -- it is whole-token wrapping, not the font size, that was leaving a line
  // half empty in front of a long path.
  overflowWrap: 'anywhere',
  hyphens: 'none',
})

// Wraps rather than ellipsizing on one line. The activity text a provider
// reports ("Running <what>", a tool name, a token tally) is the only thing that
// says WHAT the subagent is doing, and the sidebar is narrow enough that a
// single nowrap line cut almost all of it. Capped at three lines so one verbose
// row cannot push the rest of the registry off screen.
export const taskSecondary = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  overflow: 'hidden',
  display: '-webkit-box',
  WebkitBoxOrient: 'vertical',
  WebkitLineClamp: 3,
  overflowWrap: 'break-word',
  hyphens: 'auto',
})
