import { style } from '@vanilla-extract/css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: 0,
})

/** The rule between the session goal and the to-do list. */
export const separator = style({
  // Oat's base `hr` draws the line. This rule owns the equal gap on both
  // sides, so the card and list do not need matching edge padding.
  margin: 'var(--space-3) 0',
})

/** A prose objective must not expand the popover to the viewport width. */
export const popoverRoot = style({
  maxWidth: '360px',
})
