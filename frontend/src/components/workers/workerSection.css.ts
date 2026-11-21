import { style } from '@vanilla-extract/css'

/**
 * The workers list fills the width of its section, so a row clips its name.
 *
 * `sectionItems` in `~/components/workspace/workspaceList.css.ts` sizes to its
 * widest row instead, which lets the workspace tree scroll sideways to reveal a
 * deep path. A worker row has no depth to reveal that way, and that width made
 * the ellipsis on the name unreachable. This is a separate declaration rather
 * than an override of `sectionItems`, because a rule that overrides a rule in
 * another stylesheet wins or loses on the order the bundler emits them.
 */
export const workerItems = style({
  display: 'flex',
  flexDirection: 'column',
  width: '100%',
})

// The dot's shape is the shared one in `~/styles/shared.css.ts`, which the
// background tasks section uses as well. Only this palette is specific here.
export const statusConnected = style({
  background: 'var(--success)',
})

export const statusDisconnected = style({
  background: 'var(--danger)',
})

export const tunnelItem = style({
  cursor: 'default',
  paddingLeft: '20px',
})

export const tunnelIcon = style({
  flexShrink: 0,
  color: 'var(--muted-foreground)',
})

/**
 * The detail block under a stopped tunnel row: why it stopped, both key
 * fingerprints, and the two ways out. It sits in the row's own indent so it
 * reads as part of that tunnel rather than as a sibling row.
 */
export const tunnelStopped = style({
  paddingLeft: '20px',
  paddingBottom: 'var(--space-2)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const tunnelStoppedReason = style({
  color: 'var(--danger)',
})

export const tunnelFingerprints = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const tunnelStoppedActions = style({
  display: 'flex',
  gap: 'var(--space-2)',
})
