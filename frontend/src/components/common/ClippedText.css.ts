import { style } from '@vanilla-extract/css'

/**
 * The explanation line inside the tooltip, under the label it explains.
 *
 * Set apart from the label above it, so the reader sees which line repeats the
 * row and which line adds to it.
 */
export const detailLine = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})
