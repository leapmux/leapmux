import { style } from '@vanilla-extract/css'

// Sidebar variant: the section's CollapsibleSidebar content container already
// scrolls, so the list itself needs no overflow constraint.
export const taskList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
})

// Popover variant (ThinkingIndicator bg-tasks-popover): the DropdownMenu card
// does not scroll on its own, so cap the list height and scroll here to keep a
// long registry from overflowing the viewport. Applied by the popover mount in
// ./ThinkingIndicator.css.ts alongside the card styles, distinct from the
// sidebar variant above.
export const bgPopoverClass = style({
  overflowY: 'auto',
  maxHeight: '60vh',
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

// Status is carried by the dot's COLOR, so every row keeps the same glyph and
// the column reads as a status light rather than a set of shapes to learn.
export const statusDotActive = style({ color: 'var(--primary)' })
export const statusDotSuccess = style({ color: 'var(--success)' })
export const statusDotDanger = style({ color: 'var(--danger)' })
// A user's explicit stop is neither a success nor a failure.
export const statusDotMuted = style({ color: 'var(--muted-foreground)' })

export const taskBody = style({
  flex: 1,
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
})

export const taskTitle = style({
  wordBreak: 'break-word',
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
  overflowWrap: 'anywhere',
})
