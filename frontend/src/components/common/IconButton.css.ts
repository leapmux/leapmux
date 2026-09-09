import { style } from '@vanilla-extract/css'
import { iconSize } from '~/styles/tokens'

export const base = style({
  // Reset button defaults explicitly (avoid `all: unset` which breaks
  // external opacity/transition classes applied alongside this one).
  'appearance': 'none',
  'background': 'none',
  'border': 'none',
  'font': 'inherit',
  'letterSpacing': 'inherit',
  'textAlign': 'inherit',
  'textDecoration': 'none',
  'textTransform': 'inherit',
  'WebkitTapHighlightColor': 'transparent',
  'margin': 0,
  'boxSizing': 'border-box',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
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
  // Opt out of the global `button:focus-visible` inset box-shadow — small
  // icon buttons don't have room for a double ring, and their transparent
  // background already provides enough contrast against the teal outline.
  ':focus-visible': {
    boxShadow: 'none',
  },
  ':disabled': {
    opacity: 0.5,
    cursor: 'not-allowed',
  },
  'selectors': {
    '&:disabled:hover': {
      color: 'var(--muted-foreground)',
      backgroundColor: 'transparent',
    },
  },
})

export const active = style({
  backgroundColor: 'var(--card)',
  color: 'var(--foreground)',
  // Inner 1px border indicates the toggled-on state. Uses inset box-shadow
  // (not outline) so it doesn't compete with the global focus ring drawn
  // by Oat's :focus-visible rule.
  boxShadow: 'inset 0 0 0 1px var(--border)',
  selectors: {
    // When focused, drop the inner border so the outer ring renders
    // identically to the non-active case. The --card background still
    // distinguishes active from ordinary at a glance.
    '&:focus-visible': {
      boxShadow: 'none',
    },
  },
})

// Size variants, read from `iconSize.container` in `~/styles/tokens.ts`.
//
// That table is the scale, and other stylesheets already size against it, so a
// literal here would let the two drift. `sharedTree.css.ts` reserves a sidebar
// row's height from `container.md` to match the row-action button, which is the
// pairing a second spelling would silently break.
function square(size: string) {
  return style({ width: size, height: size, minWidth: size })
}

export const sizeSm = square(iconSize.container.sm)
export const sizeMd = square(iconSize.container.md)
export const sizeLg = square(iconSize.container.lg)
export const sizeXl = square(iconSize.container.xl)
