import { style } from '@vanilla-extract/css'

/**
 * The unseen-activity marker. One definition for every surface that shows it —
 * the tab strip, the mobile tab chip, and, in the sidebar, the tab row, the
 * branch, repository and workspace rows, the section header and the rail.
 *
 * The size is a fixed pixel pair on purpose: a dot is a glyph, not spacing, so
 * the `--space-N` scale does not apply to it.
 */
export const notificationDot = style({
  width: '6px',
  height: '6px',
  borderRadius: '50%',
  backgroundColor: 'var(--primary)',
  flexShrink: 0,
})
