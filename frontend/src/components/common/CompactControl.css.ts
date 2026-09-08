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

/** Shared typography and padding for compact buttons. */
export const compactControl = style(compactControlProperties)
