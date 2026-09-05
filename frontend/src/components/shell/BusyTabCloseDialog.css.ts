import { style } from '@vanilla-extract/css'

/**
 * The list of running work inside a close-confirmation dialog: process names
 * with their pids, or the active background tasks.
 *
 * Indented rather than bulleted-by-default so the rows read as evidence for the
 * sentence above them, not as a separate section.
 */
export const busyDetails = style({
  margin: `var(--space-2) 0 0`,
  paddingLeft: `var(--space-5)`,
  listStyle: 'disc',
})
