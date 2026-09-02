import { style } from '@vanilla-extract/css'

/** Shaped like the field it replaces, so a form row keeps its rhythm. */
export const trigger = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  width: '100%',
  marginBlockStart: 'var(--space-1)',
  padding: 'var(--space-2) var(--space-3)',
  fontFamily: 'var(--font-sans)',
  fontSize: 'var(--text-7)',
  lineHeight: 'var(--leading-normal)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--input)',
  borderRadius: 'var(--radius-medium)',
  cursor: 'pointer',
  textAlign: 'left',
  selectors: {
    '&:hover:not(:disabled)': {
      borderColor: 'var(--muted-foreground)',
    },
    '&:disabled': {
      cursor: 'default',
      opacity: 0.6,
    },
  },
})

/**
 * The trigger's label takes the free space, so the chevron stays at the right
 * end and the detail sits between them.
 *
 * Decoration only: `ClippedText` owns the clip rule, which is why this class
 * does not restate `overflow`, `text-overflow` or `white-space`.
 */
export const triggerLabel = style({
  flexGrow: 1,
})

/**
 * The scrolling half of a FILTERED menu.
 *
 * The filter box is the reason this exists: with the whole popover scrolling,
 * the box scrolls away with the first few rows, and the control that narrows a
 * long list is missing exactly while the list is long. So the rows scroll
 * inside their own box and the filter keeps its place above them.
 *
 * `min-height: 0` is load-bearing. A flex item's automatic minimum size is its
 * content, so without this the list refuses to shrink, the popover overflows
 * instead, and the filter box scrolls away after all.
 */
export const filteredList = style({
  flex: '1 1 auto',
  minHeight: 0,
  overflow: 'auto',
  overscrollBehavior: 'contain',
})

/** The heading `<optgroup label>` used to draw. */
export const groupHeading = style({
  padding: 'var(--space-2) var(--space-3) var(--space-1)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
})

export const filterInput = style({
  width: '100%',
  margin: 0,
  marginBlockEnd: 'var(--space-1)',
  // The box stays put while the rows scroll under it; see `filteredList`.
  flexShrink: 0,
})

export const emptyNote = style({
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})
