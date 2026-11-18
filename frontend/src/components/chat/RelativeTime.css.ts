import { style } from '@vanilla-extract/css'
import { iconSize } from '~/styles/tokens'

/**
 * The fixed-width timestamp reserves space for the clock, one space, and the widest age.
 * The visible clock and age share a grid cell with the hidden measurement and align to
 * its end. The pseudo-element measures the widest age without adding text to the page.
 */
export const steadyTime = style({
  'display': 'inline-grid',
  'justifyItems': 'end',
  'fontVariantNumeric': 'tabular-nums',
  '::before': {
    content: '" " attr(data-widest-age)',
    whiteSpace: 'pre',
    paddingInlineStart: `${iconSize.xs}px`,
    visibility: 'hidden',
    gridArea: '1 / 1',
  },
})

export const steadyTimeContent = style({
  display: 'inline-flex',
  alignItems: 'center',
  whiteSpace: 'pre',
  gridArea: '1 / 1',
})
