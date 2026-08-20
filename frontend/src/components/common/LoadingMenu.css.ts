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

export const triggerText = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
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
})

export const emptyNote = style({
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})
