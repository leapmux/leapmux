import { style } from '@vanilla-extract/css'
import { popoverPanelClamp } from '~/styles/popover.css'

/**
 * The panel's shell: the tab bar, and the scrolling region it swaps.
 *
 * The row styles live beside the component that renders them, in
 * `./BackgroundTaskList.css.ts`; this file holds only what the panel itself
 * owns, so a change to a row cannot reflow the panel and vice versa.
 */
export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflow: 'hidden',
})

/**
 * Sidebar variant: fill the section's content box.
 *
 * That box scrolls on its own, and letting it do so would carry the tab bar off
 * the top with the rows. A definite height hands the scrolling to `rows` below
 * instead, and the outer container then never has anything to scroll.
 */
export const sidebarRoot = style({
  height: '100%',
})

/**
 * Popover variant (the ThinkingIndicator's bg-tasks popover).
 *
 * Both axes need a cap, for different reasons: a long registry overflows the
 * card vertically, and a row holds each of its two lines on one line, so a long
 * shell command asks for the full width of the command. `popoverPanelClamp` in
 * `~/styles/popover.css.ts` owns both numbers, so this panel and the Goals &
 * To-dos panel beside it cannot reach different sizes.
 */
export const popoverRoot = popoverPanelClamp

/**
 * The scrolling region the kind tabs swap.
 *
 * `overflow-x: hidden` is declared, not left out. `overflow-y: auto` alone makes
 * CSS compute the other axis from `visible` to `auto`, so this box grew a
 * horizontal scrollbar for any descendant that exceeded it. Every row now clips
 * its own text, and this makes that structural: no descendant added later can
 * bring the sideways scroll back.
 */
export const rows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
  overflowY: 'auto',
  overflowX: 'hidden',
  minHeight: 0,
})
