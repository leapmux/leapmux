import { globalStyle, style } from '@vanilla-extract/css'

/**
 * The age text of a RelativeTime that keeps one width at every age (its `steadyWidth`
 * option). The text and a hidden copy of the widest age share the one cell of a grid, so
 * the cell is as wide as the widest age, and the text sits at its end. The copy is a
 * pseudo-element that holds the text of the `data-widest-age` attribute, so it adds
 * nothing to the text of the page. Tabular digits give every digit one width, so the
 * widest age covers every age of as many digits.
 */
export const steadyAgeText = style({
  'display': 'inline-grid',
  'justifyItems': 'end',
  'fontVariantNumeric': 'tabular-nums',
  '::before': {
    content: 'attr(data-widest-age)',
    visibility: 'hidden',
    gridArea: '1 / 1',
  },
})

globalStyle(`${steadyAgeText} > *`, {
  gridArea: '1 / 1',
})
