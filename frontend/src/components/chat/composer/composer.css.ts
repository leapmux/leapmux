import { globalStyle, style } from '@vanilla-extract/css'
import { popoverBase } from '~/styles/popover.css'
import { chipBase } from '~/styles/shared.css'
import { breakpoints } from '~/styles/tokens'

/**
 * Styles for the simplified composer: the input box's `[+]` button, the
 * status-bar chips (branch / model / effort / mode), and the popovers/submenus
 * the `[+]` menu and chips open. The status bar itself lives outside the box
 * (full-width, unbordered); the box's own border/hover styling continues to
 * come from the `container` style in `../markdownEditor/MarkdownEditor.css.ts`.
 */

// --- Status bar ---

export const statusBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  padding: '0 var(--space-3) var(--space-2)',
  flexShrink: 0,
})

export const statusBarLeft = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  // Allow shrinking so the right cluster (limit/context) is always visible.
  // Chips truncate via their own maxWidth/ellipsis rather than wrapping.
  minWidth: 0,
  overflow: 'hidden',
})

export const statusBarRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  flexShrink: 0,
})

// Make ot-dropdown (the DropdownMenu/BranchContextMenu wrapper) participate in
// flex alignment so the chip buttons inside align consistently. Without this,
// ot-dropdown defaults to display:inline, causing baseline misalignment between
// chips with leading icons (branch, effort) and text-only chips (model, mode).
globalStyle(`${statusBar} ot-dropdown`, {
  display: 'inline-flex',
  alignItems: 'center',
})

// Hide lower-priority chips on narrow screens. Two tiers, not a ladder: Branch
// and Model always stay, and Mode and Effort both drop at the `sm` breakpoint.
// The chips that drop carry `data-chip-optional`.
//
// The rule hooks that attribute, not a `data-testid`: a test id is not a layout
// contract, so renaming one would disable the responsive rule with no compile
// error and no failing test.
globalStyle(`${statusBar} [data-chip-optional]`, {
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: { display: 'none' },
  },
})

// --- Chips (branch / model / effort / mode) ---

// A chip is a small, muted, hover-affordant button that opens a popover. It
// reuses the visual register of the old fused settings trigger (faint text,
// hover -> foreground + card bg) so the status bar reads as the same surface,
// just split into independent axes.
export const axisChip = style([chipBase, {
  gap: 'var(--space-1)',
  padding: '2px var(--space-1)',
  fontSize: 'var(--text-8)',
  lineHeight: 1,
  whiteSpace: 'nowrap',
  userSelect: 'none',
}])

export const axisChipLabel = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  maxWidth: '14em',
})

// --- Popovers ---

/**
 * The shared card surface for every composer popover: the positioning reset
 * plus the background, border, radius, shadow, and viewport clamp.
 *
 * Each popover below composes this and adds only what genuinely differs — its
 * stacking order, its minimum width, and its padding. Padding differs by
 * CONTENT, not by decoration: a menu list pads by `--space-1` because its items
 * carry their own padding, while a content card needs real inset.
 */
const popoverCard = style([popoverBase, {
  flexDirection: 'column',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-large)',
  maxWidth: 'calc(100vw - var(--space-4) * 2)',
  maxHeight: 'calc(100vh - var(--space-6) * 2)',
  overflowY: 'auto',
}])

// A chip's popover hosts OptionGroupMenuItems (radio menu items, or a
// filterable listbox once the group exceeds the searchable threshold).
export const axisPopover = style([popoverCard, {
  padding: 'var(--space-1)',
  zIndex: 300,
  minWidth: '200px',
}])

// --- `[+]` button ---

export const plusButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  // Sized to match the single-line text area height (--composer-btn-h, defined
  // on the MarkdownEditor container), kept square.
  width: 'var(--composer-btn-h)',
  height: 'var(--composer-btn-h)',
  borderRadius: 'var(--radius-small)',
  color: 'var(--muted-foreground)',
  backgroundColor: 'var(--muted)',
  cursor: 'pointer',
  flexShrink: 0,
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--secondary)' },
  },
})

export const plusPopover = style([popoverCard, {
  padding: 'var(--space-1)',
  zIndex: 300,
  minWidth: '220px',
}])

// --- Submenu (nested DropdownMenu for each option group under `[+]` ▸ settings) ---

export const subTrigger = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  width: '100%',
})

/**
 * The leading half of a submenu trigger, for an item whose label carries an icon
 * (the branch item). `subTrigger` pushes its two children apart, so the icon and
 * the text need one box between them or the chevron separates them instead.
 */
export const subTriggerLabel = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const subPopover = style([popoverCard, {
  padding: 'var(--space-1)',
  zIndex: 310,
  minWidth: '200px',
}])

/**
 * The `[+]` ▸ "Agent info" submenu. Its content is a card of labelled rows, not
 * a list of menu items, so it needs real inset padding -- the `--space-1` the
 * menu popovers use is the gutter around items that pad themselves, and it
 * leaves free-standing rows flush against the border.
 */
export const infoPopover = style([popoverCard, {
  padding: 'var(--space-3)',
  zIndex: 310,
  minWidth: '240px',
}])
