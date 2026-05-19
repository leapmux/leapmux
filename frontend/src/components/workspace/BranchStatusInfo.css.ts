import { style } from '@vanilla-extract/css'

// Tight column for the branch-status lines so the dialog body's
// `vstack gap-4` (16px between top-level children) cleanly separates the
// status block from neighbouring controls, while the lines inside the
// block sit closer together (8px). Without this, the lines were `<p>`
// children of the parent's gap-4 vstack — Oat's base `p { margin-block-end:
// var(--space-4) }` stacked on the flex gap, doubling the visible spacing
// to ~32px between every line.
export const statusLines = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})
