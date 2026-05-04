import { style } from '@vanilla-extract/css'

// Bump the minimum dialog width so the footer row (Cancel / Send email /
// Copy command) stays on a single line. The default 360px wraps the third
// button onto its own line once the email button label expands to
// "Sent to <addr>".
export const dialog = style({
  'minWidth': '560px',
  '@media': {
    '(max-width: 639px)': {
      minWidth: 'unset',
    },
  },
})

export const body = style({
  gap: 'var(--space-3)',
})

export const command = style({
  backgroundColor: 'var(--muted)',
  padding: 'var(--space-3)',
  borderRadius: 'var(--radius)',
  fontFamily: 'var(--font-mono)',
  overflowX: 'auto',
  userSelect: 'all',
})
