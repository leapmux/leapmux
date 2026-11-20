import { style } from '@vanilla-extract/css'

export const typeSelector = style({
  display: 'flex',
  gap: 'var(--space-4)',
  marginBottom: 'var(--space-4)',
})

export const typeOption = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const fieldRow = style({
  display: 'grid',
  gridTemplateColumns: '1fr 1fr',
  gap: 'var(--space-3)',
  marginBottom: 'var(--space-3)',
})

export const fieldGroup = style({
  marginBottom: 'var(--space-3)',
})

/**
 * The Worker key trust line and its verify button. The state text is not an
 * interactive control, so it needs no tooltip; the button shows a word and
 * names itself.
 */
export const trustRow = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  marginBottom: 'var(--space-3)',
})

/** The Worker key mismatch a create was refused with: the reason and both fingerprints. */
export const keyMismatch = style({
  marginBottom: 'var(--space-3)',
  padding: 'var(--space-3)',
  border: '1px solid var(--danger)',
  borderRadius: '4px',
})
