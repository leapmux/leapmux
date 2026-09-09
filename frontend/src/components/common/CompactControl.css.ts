import { style } from '@vanilla-extract/css'

const COMPACT_FONT_SIZE = 'var(--text-8)'
const COMPACT_PADDING_BLOCK = 'var(--space-1)'
const COMPACT_PADDING_INLINE = 'var(--space-3)'
const CONTROL_BORDER_HEIGHT = '2px'

/** The natural height of a compact button with one line of text. */
export const compactControlHeight = `calc(${COMPACT_FONT_SIZE} * var(--leading-normal) + ${COMPACT_PADDING_BLOCK} * 2 + ${CONTROL_BORDER_HEIGHT})`

/** Shared typography and padding rules for compact controls. */
export const compactControlProperties = {
  padding: `${COMPACT_PADDING_BLOCK} ${COMPACT_PADDING_INLINE}`,
  fontSize: COMPACT_FONT_SIZE,
} as const

/**
 * Shared typography, padding, and HEIGHT for a compact button.
 *
 * The height is pinned because a button is `inline-flex` (Oat's rule), and a
 * flex container has no line-box strut: its content height is the tallest ITEM.
 * A button showing one line of text is as tall as that line box, and the same
 * button showing a 14px icon alone is 4px shorter -- which is what the composer
 * does below `sm`, where `hideInNarrowComposer` takes the word away from Pause
 * Queue, Interrupt and Send. Those three then no longer matched the `[+]`
 * button beside them, whose `--editor-btn-h` is this very value.
 *
 * `minHeight` rather than `height`, so a caller that does let a label wrap
 * still grows.
 *
 * The pill options are NOT covered, and must not be: they spread
 * `compactControlProperties` while their GROUP owns the border this height
 * counts, so pinning them here would make every small pill group 2px taller.
 * A group is `inline-flex` at the default `align-items: stretch`, so its
 * options already match the tallest of them.
 */
export const compactControl = style({
  ...compactControlProperties,
  minHeight: compactControlHeight,
  // Never compress. A decision button must stay readable at every width, so the
  // row gives way somewhere else: the control request's options cluster scrolls,
  // and the composer's own cluster wraps.
  flexShrink: 0,
})
