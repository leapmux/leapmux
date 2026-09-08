import { style } from '@vanilla-extract/css'
import { popoverPanelClamp } from '~/styles/popover.css'

/**
 * The stack: the goal card, the rule, and the to-do list.
 *
 * `paddingBottom` because `GoalCard` deliberately spends none at its bottom
 * edge -- the separator owns the gap on both of its sides, and a second
 * contributor to one gap is what forced that margin to be stated
 * asymmetrically before. When no list follows there IS no separator, so
 * without this the card's counters row sits flush against the section's bottom
 * edge: the sidebar's own content box adds no padding of its own.
 */
export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: 0,
  paddingBottom: 'var(--space-1)',
})

/** The rule between the session goal and the to-do list. */
export const separator = style({
  // Oat's base `hr` draws the line. This rule owns the equal gap on both
  // sides, so the card and list do not need matching edge padding.
  margin: 'var(--space-3) 0',
})

/**
 * Popover variant. A prose objective must not expand the card to the viewport
 * width, and a long to-do list under one must not run to its full height. Both
 * numbers come from `~/styles/popover.css.ts`, which the background-task panel
 * reads too, so the two popovers cannot reach different sizes.
 */
export const popoverRoot = popoverPanelClamp
