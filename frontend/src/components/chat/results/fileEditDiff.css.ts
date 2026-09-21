import { style } from '@vanilla-extract/css'

/** Keep the file title identical in the tool header and the multi-file body. */
export const fileEditTitle = style({
  display: 'inline-flex',
  alignItems: 'baseline',
  gap: 'var(--space-1)',
  minWidth: 0,
  color: 'var(--foreground)',
  fontFamily: 'var(--font-sans)',
  fontSize: 'var(--text-7)',
  lineHeight: 1.6,
})

/** Keep the statistics visible when a long file path uses the available width. */
export const fileEditStats = style({
  flexShrink: 0,
  color: 'var(--foreground)',
  whiteSpace: 'nowrap',
})
