import { style } from '@vanilla-extract/css'

/**
 * One provider glyph, as a 24px square button.
 *
 * Lives here rather than beside the tab bar because it has two homes now: this
 * component's icon row, and the tab bar's own most-recently-used strip. One
 * style, so the two cannot drift into different-sized icons for the same glyph.
 */
export const providerButton = style({
  'appearance': 'none',
  'background': 'none',
  'border': 'none',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '24px',
  'height': '24px',
  'minWidth': '24px',
  'padding': 0,
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'flexShrink': 0,
  'lineHeight': 0,
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--card)',
  },
})

/** The menu row that holds every provider glyph. Wraps on a narrow menu. */
export const providerIconsRow = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-1) var(--space-3)',
})

/** The `(default)` note beside the shell the worker starts by default. */
export const shellDefault = style({
  marginLeft: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})
