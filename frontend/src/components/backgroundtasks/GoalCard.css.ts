import { style } from '@vanilla-extract/css'

/** The goal card: a heading, the objective, its status, and the verb buttons. */
export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  padding: 'var(--space-2)',
  borderBottom: '1px solid var(--border-color)',
})

/**
 * The card's own heading.
 *
 * The section header above it is the user-renameable `section.name`, so it may
 * say anything at all -- the card cannot borrow it to say what it is.
 */
export const heading = style({
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
})

export const objective = style({
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  // The objective is prose and may be a paragraph. It wraps rather than
  // clipping, because a goal the reader cannot read is the one thing this card
  // exists to show.
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
})

export const statusRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

/** The progress counters: only the ones the provider actually reported. */
export const meta = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
})

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-1)',
  marginTop: 'var(--space-1)',
})

export const action = style({
  fontSize: 'var(--text-8)',
  padding: 'var(--space-1) var(--space-2)',
  borderRadius: '4px',
  border: '1px solid var(--border-color)',
  background: 'transparent',
  color: 'var(--foreground)',
  cursor: 'pointer',
  selectors: {
    '&:hover:not(:disabled)': { background: 'var(--subtle-background)' },
    '&:disabled': { opacity: 0.5, cursor: 'default' },
  },
})

/** The empty state on the Goals tab, where a goal can still be set. */
export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: 'var(--space-2)',
  padding: 'var(--space-4) var(--space-2)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})

/**
 * The live region that announces a status change.
 *
 * Offscreen rather than hidden: `display: none` and `visibility: hidden` both
 * take a live region out of the accessibility tree, so nothing is announced.
 */
export const liveRegion = style({
  position: 'absolute',
  width: '1px',
  height: '1px',
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
})
