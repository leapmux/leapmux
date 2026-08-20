import { style } from '@vanilla-extract/css'

/**
 * A theme's colours as a chip: its background, with nine of its palette tokens
 * as a 3x3 block of pips on top.
 *
 * Fixed pixel geometry, not a spacing token: this is an icon-sized graphic
 * whose proportions are its own, the way the scrollbar and resizer widths are.
 * The 1px padding is below `--space-1` (4px), which would swallow the pips.
 *
 * The padding and the border are what make the background readable. Oat sets
 * `box-sizing: border-box` globally, so 16px less the 1px border and the 1px
 * padding on each side leaves a 12px block of pips -- and the palette's
 * background shows as a ring around them and in all eight gaps between them.
 */
export const swatch = style({
  flexShrink: 0,
  width: '16px',
  height: '16px',
  padding: '1px',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  overflow: 'hidden',
})

/**
 * The pips fill the chip's content box.
 *
 * `display: block` because an inline SVG sits on the text baseline, which would
 * leave a descender's worth of space below it inside the fixed-height chip.
 */
export const swatchGrid = style({
  display: 'block',
  width: '100%',
  height: '100%',
})
