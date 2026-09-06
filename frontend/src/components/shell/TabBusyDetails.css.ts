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

/**
 * The per-tab blocks when one prompt covers a GROUP of busy tabs, as the
 * delete-branch dialog does. Each block names its tab and then states that tab's
 * reason, so the reader can tell whose work is whose.
 */
export const busyGroupList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: `var(--space-3)`,
  marginTop: `var(--space-3)`,
})
