import { keyframes, style } from '@vanilla-extract/css'

const spin = keyframes({
  '100%': { transform: 'rotate(360deg)' },
})

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

export const spinIcon = style({
  animation: `${spin} 1s linear infinite`,
})

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

export const taskSecondary = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
})

export const parentChip = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})
