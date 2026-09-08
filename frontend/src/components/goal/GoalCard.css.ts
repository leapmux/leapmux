import { style } from '@vanilla-extract/css'

/**
 * The goal card: a header, the objective, its status, and its counters.
 *
 * No rule of its own. A separator states that something FOLLOWS, and only the
 * host knows whether anything does. GoalsAndTodos owns that separator.
 */
export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  // No padding at the BOTTOM. What sits under the card is the separator, and
  // the separator owns the space on both sides. See GoalsAndTodos.separator.
  // Padding here would be a second contributor to one gap, which is what forced
  // the rule's own margin to be stated asymmetrically to compensate.
  padding: 'var(--space-2) var(--space-2) 0',
})

/**
 * The card's own header: what this card is, and its `...` menu at the far end.
 *
 * The heading takes the squeeze and the trigger keeps its size, which is what
 * the sidebar's own section header does with its actions.
 */
export const headerRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  minWidth: 0,
})

/**
 * The card's own heading.
 *
 * The section header above it is the user-renameable `section.name`, so it may
 * say anything at all -- the card cannot borrow it to say what it is.
 */
export const heading = style({
  flex: 1,
  minWidth: 0,
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
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

/**
 * The empty state where a goal can still be set.
 *
 * Laid out to match the populated card exactly, because the two swap in the
 * same slot and a reader watching a goal arrive should see the rows change and
 * nothing move. So: no padding of its own -- the card supplies every edge --
 * the same `space-1` between rows that the card puts between the objective,
 * the status and the counters, and the same `text-7` on its first line as the
 * objective it stands in for.
 *
 * `align-items` is the one genuine difference. The card lets its rows stretch,
 * which is right for a line of text and wrong for a button: stretched, the call
 * to action would span the whole sidebar.
 */
export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: 'var(--space-1)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})
