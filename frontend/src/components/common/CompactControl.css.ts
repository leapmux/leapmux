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
 * Shared typography, padding, and height for a compact button.
 *
 * Oat uses inline-flex buttons, whose tallest item determines their content height.
 * A 14px icon alone is 4px shorter than a text label.
 * The minimum height keeps icon-only Pause Queue, Interrupt, and Send aligned with [+]
 * when hideInNarrowComposer removes their labels. The adjacent [+] uses this height through --editor-btn-h.
 *
 * The minimum permits a taller button when a caller allows its label to wrap.
 *
 * Pill options use compactControlProperties because their group owns the border.
 * This minimum would count that border twice and make small pill groups 2px taller.
 * Each group's default stretch alignment gives its options the height of the tallest option.
 */
export const compactControl = style({
  ...compactControlProperties,
  minHeight: compactControlHeight,
  // Buttons stay readable. The control request row scrolls, and the composer action group wraps.
  flexShrink: 0,
})
