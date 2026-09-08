import { style } from '@vanilla-extract/css'

/**
 * The work panel's shell: the tab bar, and the scrolling region it swaps.
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
 * The DropdownMenu card sizes to its content, so capping the list is what caps
 * the card. Both axes need a cap, for different reasons: a long registry
 * overflows the card vertically, and a row holds each of its two lines on one
 * line, so a long shell command asks for the full width of the command.
 *
 * Neither cap restates the VIEWPORT clamp: `popoverCard` in
 * `~/styles/popover.css.ts` already holds the card inside the viewport on both
 * axes, and Oat's global `box-sizing: border-box` means its own padding comes
 * out of that. These two are the tighter, content-shaped limits on top.
 */
export const popoverRoot = style({
  maxHeight: '60vh',
  maxWidth: '360px',
})

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

/**
 * The rule between the session goal and the task rows.
 *
 * The PANEL owns it, not the card, because a separator states that something
 * FOLLOWS and only the panel knows whether anything does. The card shows on two
 * tabs: above the rows on All, where the rule separates them, and alone on
 * Goal, where it is the last thing in the box and a rule underlines nothing.
 *
 * A real `<hr>` between the two, rather than a border on the card. A border
 * belongs to the box that draws it, so the card would have to take a class to
 * say what sits under it, and the space below the rule would have to be a
 * margin -- padding sits INSIDE the border box and only pushes the line further
 * from the card's own content. An element takes the gap on both sides by
 * itself.
 */
export const goalSeparator = style({
  // Oat's base `hr` already draws the line (`border: none` plus
  // `border-top: 1px solid var(--border)`); only its `var(--space-8)` margin is
  // wrong at this size.
  //
  // The rule owns the whole gap on BOTH sides, so the two are one value and
  // cannot drift apart. That is why `GoalCard` spends no padding at its bottom
  // edge: two contributors to one gap is what forced this margin to be stated
  // asymmetrically before, and it made the answer depend on which of the two
  // you edited. `rows` adds its own 2px gap to each side alike.
  margin: 'var(--space-3) 0',
})
