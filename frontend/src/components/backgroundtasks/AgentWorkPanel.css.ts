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

// Shown in place of the rows when the selected kind tab has none. Without it a
// tab with no rows renders an empty box that reads as a rendering fault.
export const emptyMessage = style({
  padding: 'var(--space-4) var(--space-2)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  textAlign: 'center',
})

// The same box when the registry could not be LOADED. Carries the danger colour
// because it reports a fault rather than an absence, and the two otherwise read
// identically.
export const loadFailedMessage = style({
  color: 'var(--danger)',
})

// Decoration only. `ClippedText` owns the clipping rule -- see
// `~/components/common/ClippedText.tsx`.
//
// `display: block` is defensive, not load-bearing. The header is a <span>, and
// an inline box would drop the vertical padding below; but the header renders
// into `rows`, which is a flex container, and CSS blockifies a flex item
// already. This declaration only holds the padding if `rows` stops being a flex
// container.
