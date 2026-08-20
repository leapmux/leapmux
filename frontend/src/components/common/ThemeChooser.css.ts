import { style } from '@vanilla-extract/css'
import { breakpoints } from '~/styles/tokens'

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-3)',
  flexWrap: 'wrap',
  // Left-aligned by default, which is where the Preferences dialog puts every
  // other row's control. The centred layouts ask for the other alignment.
  justifyContent: 'flex-start',
  selectors: {
    '&[data-align="center"]': {
      justifyContent: 'center',
    },
  },
})

export const label = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  whiteSpace: 'nowrap',
})

/**
 * The menu trigger, shaped like `SettingRow`'s scope chip so the two read as
 * one family in the Preferences dialog.
 *
 * Deliberately NOT the scope chip's own style: that one is a pill for a
 * secondary, text-only affordance, while this holds a swatch and is the row's
 * primary control. Sharing the class would tie the two to each other's future.
 */
export const trigger = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  fontFamily: 'inherit',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  cursor: 'pointer',
  minWidth: 0,
  selectors: {
    '&:hover:not(:disabled)': {
      borderColor: 'var(--muted-foreground)',
    },
    // Governed by "Match UI": dimmed to the same degree as the mode pills
    // beside it, so the row reads as one governed group.
    '&:disabled': {
      cursor: 'default',
      opacity: 0.55,
    },
  },
})

/** The trigger's name. Clipped rather than wrapped, so the row stays one line. */
export const triggerText = style({
  'overflow': 'hidden',
  'textOverflow': 'ellipsis',
  'whiteSpace': 'nowrap',
  // Enough for the longest label the picker carries ("Default (Dimidium)")
  // without letting a future one stretch the row.
  'maxWidth': '11rem',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      maxWidth: '7rem',
    },
  },
})

/** Heads the light and dark halves of the variant menu when both offer a pick. */
export const variantGroup = style({
  padding: 'var(--space-2) var(--space-3) var(--space-1)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
})
