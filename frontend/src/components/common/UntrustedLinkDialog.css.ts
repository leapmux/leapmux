import { style } from '@vanilla-extract/css'

/**
 * The address itself, and the text that the terminal shows for it.
 *
 * Monospace, because the whole point of the prompt is that the reader compares
 * two strings character by character: a proportional font hides the difference
 * between `rn` and `m`, and between `l` and `I`. `break-all` keeps a long
 * address inside the dialog instead of widening it off the screen.
 */
export const linkValue = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  wordBreak: 'break-all',
  userSelect: 'text',
})

/** The second-order warning under the two addresses. */
export const note = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})
