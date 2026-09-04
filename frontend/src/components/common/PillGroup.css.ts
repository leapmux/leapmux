import { style } from '@vanilla-extract/css'

/** One content-sized control with one outer border. */
export const pillGroup = style({
  position: 'relative',
  isolation: 'isolate',
  display: 'inline-flex',
  width: 'max-content',
  gap: 0,
  overflow: 'hidden',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

/**
 * Dim a governed group as one control. The active value remains readable, and
 * the pointer shows that this control refuses input.
 */
export const pillGroupDisabled = style({
  opacity: 0.55,
})

/** The primary fill that moves between the segments. */
export const selectionIndicator = style({
  position: 'absolute',
  insetBlock: 0,
  left: 0,
  zIndex: 0,
  backgroundColor: 'var(--primary)',
  pointerEvents: 'none',
  transitionProperty: 'none',
})

/** Dim the fill when the selected option refuses input on its own. */
export const selectionIndicatorDimmed = style({
  opacity: 0.55,
})

/** Enable motion only for a selection change after the first measurement. */
export const selectionIndicatorMoves = style({
  'transition': 'transform var(--transition), width var(--transition)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transitionProperty: 'none',
    },
  },
})

/** Shape and metrics that each segment shares. */
export const pillOption = style({
  position: 'relative',
  zIndex: 1,
  flexShrink: 0,
  padding: 'var(--space-2) var(--space-4)',
  border: 0,
  borderRadius: 0,
  backgroundColor: 'transparent',
  color: 'var(--muted-foreground)',
  fontWeight: 'var(--font-normal)',
  opacity: 1,
  cursor: 'pointer',
  selectors: {
    '&:hover:not(:disabled)': {
      backgroundColor: 'var(--accent)',
      color: 'var(--foreground)',
    },
    '&:active:not(:disabled)': {
      transform: 'none',
    },
    // The group clips its contents to the outer radius. Keep the focus ring
    // inside each segment so clipping cannot hide it.
    '&:focus-visible': {
      outline: '2px solid var(--ring)',
      outlineOffset: '-2px',
    },
    '&:disabled': {
      cursor: 'default',
    },
  },
})

/** Keep each boundary visible above the moving fill. */
export const pillOptionSeparated = style({
  borderInlineStart: '1px solid var(--border)',
})

/** Dim one refused option without dimming the other segments. */
export const pillOptionDimmed = style({
  opacity: 0.55,
})

/**
 * The moving indicator supplies this segment's background.
 *
 * Read the label from the palette. A literal white label is unreadable on many
 * light primary colors. The theme tests verify this color pair.
 */
export const pillOptionActive = style({
  color: 'var(--primary-foreground)',
  selectors: {
    '&:hover:not(:disabled)': {
      backgroundColor: 'transparent',
      color: 'var(--primary-foreground)',
    },
  },
})
