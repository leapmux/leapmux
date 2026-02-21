import { style } from '@vanilla-extract/css'

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
  'outline': 'none',
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
  borderColor: 'var(--border)',
})

// Size variants

export const sizeSm = style({ width: '20px', height: '20px', minWidth: '20px' })
export const sizeMd = style({ width: '24px', height: '24px', minWidth: '24px' })
export const sizeLg = style({ width: '28px', height: '28px', minWidth: '28px' })
export const sizeXl = style({ width: '36px', height: '36px', minWidth: '36px' })
