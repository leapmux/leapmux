import { style } from '@vanilla-extract/css'

/*
 * The Network access panel's vocabulary.
 *
 * No outer border and no heading: this editor is the control of ONE
 * `SettingRow`, and the row supplies the label, the help text and the
 * separator beneath it -- the rule `./../account/accountFields.css.ts`
 * states for every account editor.
 */

/** A small-caps heading over one list inside the panel. */
export const sectionHeading = style({
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-medium)',
  textTransform: 'uppercase',
  letterSpacing: '0.06em',
  color: 'var(--muted-foreground)',
})

/** The read-only "serving now" list. */
export const servingList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  margin: 0,
  padding: 0,
  listStyle: 'none',
})

export const servingRow = style({
  display: 'flex',
  alignItems: 'baseline',
  gap: 'var(--space-2)',
  fontSize: 'var(--text-7)',
})

/** The address itself, in the font a copied command is read in. */
export const servingAddress = style({
  fontFamily: 'var(--font-mono)',
  color: 'var(--foreground)',
})

/** Why the hub serves an address: from -listen, an extra, or both merged. */
export const servingNote = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

/** One editable address: the interface menu, the port, and the remove button. */
export const addressRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

/**
 * The interface trigger takes the free width and the port stays its own size,
 * so a long link-local address does not push the port off the row.
 */
export const interfaceTrigger = style({
  flex: 1,
  minWidth: 0,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  textAlign: 'left',
})

/** Fixed, because a port is at most five digits and the column should not jump. */
export const portInput = style({
  width: '6.5rem',
  fontFamily: 'var(--font-mono)',
  /*
   * Oat gives every input a `margin-block-start` for the gap under its label.
   * This one has no label above it -- it sits in a flex row beside two buttons
   * -- and `align-items: center` centres the MARGIN box, so that margin pushed
   * the field half a step below the interface menu and the remove button.
   */
  marginBlockStart: 0,
})

/**
 * The remove button, narrowed to its icon.
 *
 * Only the horizontal padding changes: the vertical padding is what makes this
 * button the same height as the port field beside it, and Oat's `.icon` class
 * drops padding on both axes.
 */
export const removeButton = style({
  paddingInline: 'var(--space-3)',
})

/** The preview of what the hub will serve after Apply. */
export const preview = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const previewAddresses = style({
  fontFamily: 'var(--font-mono)',
  color: 'var(--foreground)',
})

/** The menu's per-interface grouping header. */
export const menuGroupHeader = style({
  padding: 'var(--space-1) var(--space-2)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-medium)',
  color: 'var(--muted-foreground)',
})
