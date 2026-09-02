import { style } from '@vanilla-extract/css'
import { popoverBase } from '~/styles/popover.css'

/**
 * The custom property that carries the TRIGGER's measured width to the popover.
 *
 * Written by `LoadingMenu`, which measures the trigger and sets it on the
 * popover element; read by `popover` below. The name is spelled here and read
 * through this constant at the one call site, so the two cannot drift.
 *
 * A custom property rather than an inline `max-width`, because the cap is one
 * term of a `min()` that also holds the viewport clamp: written as a whole
 * declaration from TypeScript, that arithmetic would live in the component and
 * not in the stylesheet.
 */
export const TRIGGER_WIDTH_VAR = '--loading-menu-trigger-width'

/** The trigger's width, or the viewport when nothing measured it yet. */
const triggerWidth = `var(${TRIGGER_WIDTH_VAR}, 100vw)`

/**
 * The custom property that carries the height of the DIALOG holding the trigger.
 *
 * Written by `LoadingMenu` the same way as the width above, and absent for a
 * menu that no dialog holds -- a sidebar selector, a toolbar -- which then keeps
 * the viewport clamp alone.
 */
export const DIALOG_HEIGHT_VAR = '--loading-menu-dialog-height'

/** The dialog's height, or the viewport when the menu is in no dialog. */
const dialogHeight = `var(${DIALOG_HEIGHT_VAR}, 100vh)`

/** Shaped like the field it replaces, so a form row keeps its rhythm. */
export const trigger = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
  width: '100%',
  marginBlockStart: 'var(--space-1)',
  padding: 'var(--space-2) var(--space-3)',
  fontFamily: 'var(--font-sans)',
  fontSize: 'var(--text-7)',
  lineHeight: 'var(--leading-normal)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--input)',
  borderRadius: 'var(--radius-medium)',
  cursor: 'pointer',
  textAlign: 'left',
  selectors: {
    '&:hover:not(:disabled)': {
      borderColor: 'var(--muted-foreground)',
    },
    '&:disabled': {
      cursor: 'default',
      opacity: 0.6,
    },
  },
})

/**
 * The trigger's label takes the free space, so the chevron stays at the right
 * end and the detail sits between them.
 *
 * Decoration only: `ClippedText` owns the clip rule, which is why this class
 * does not restate `overflow`, `text-overflow` or `white-space`.
 */
export const triggerLabel = style({
  flexGrow: 1,
})

/**
 * The menu's own popover box: never wider than its trigger, never taller than
 * the viewport, and scrollable on both axes.
 *
 * Without it the box has no limit in either direction. `calcPopoverPosition`
 * clamps where the popover STARTS, not how large it grows, so a directory with
 * fifty resumable sessions ran off the bottom of the screen with the rows below
 * the fold unreachable, and one long session title made the menu wider than the
 * dialog that holds it.
 *
 * Three rules, and each answers one of those:
 *
 *  - The width follows the TRIGGER. A menu is the open form of the control the
 *    user clicked, so it reads as one control when the two edges line up, and a
 *    title longer than the field clips instead of pushing the box out over the
 *    page. `min()` keeps the viewport clamp as well, for a trigger that is
 *    itself near the width of a phone screen.
 *  - `min-width` is restated because Oat's own `ot-dropdown [popover]` rule sets
 *    `min-width: 12rem`, and a min-width ALWAYS beats a max-width. A trigger
 *    narrower than 12rem would keep a popover wider than itself, which is
 *    exactly the case the cap exists for.
 *  - The height follows the DIALOG that holds the trigger, not the viewport. A
 *    dialog is a box a reader takes in as one thing, and a menu taller than it
 *    reads as a second window over the page rather than as the open form of a
 *    field inside it. The viewport clamp stays as the outer term, for a menu no
 *    dialog holds and for a dialog as tall as the screen.
 *  - `overflow: auto` on both axes. The vertical half is what makes a long list
 *    reachable; the horizontal half is the swipe that reaches a row too wide to
 *    fit -- a narrow field on a phone, where the checked-state radio and the
 *    age take room the title cannot give up.
 *
 * NOT `popoverColumnClamp`: that class caps both axes at the viewport, which is
 * the answer for a card that has neither a trigger nor a dialog to follow.
 */
export const popover = style([popoverBase, {
  flexDirection: 'column',
  maxWidth: `min(${triggerWidth}, calc(100vw - var(--space-4) * 2))`,
  minWidth: `min(12rem, ${triggerWidth})`,
  maxHeight: `min(${dialogHeight}, calc(100vh - var(--space-6) * 2))`,
  overflow: 'auto',
  // A swipe that reaches the end of the list must not carry on into the dialog
  // behind the menu.
  overscrollBehavior: 'contain',
}])

/**
 * The scrolling half of a FILTERED menu.
 *
 * The filter box is the reason this exists: with the whole popover scrolling,
 * the box scrolls away with the first few rows, and the control that narrows a
 * long list is missing exactly while the list is long. So the rows scroll
 * inside their own box and the filter keeps its place above them.
 *
 * `min-height: 0` is load-bearing. A flex item's automatic minimum size is its
 * content, so without this the list refuses to shrink, the popover overflows
 * instead, and the filter box scrolls away after all.
 */
export const filteredList = style({
  flex: '1 1 auto',
  minHeight: 0,
  overflow: 'auto',
  overscrollBehavior: 'contain',
})

/** The heading `<optgroup label>` used to draw. */
export const groupHeading = style({
  padding: 'var(--space-2) var(--space-3) var(--space-1)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
})

export const filterInput = style({
  width: '100%',
  margin: 0,
  marginBlockEnd: 'var(--space-1)',
  // The box stays put while the rows scroll under it; see `filteredList`.
  flexShrink: 0,
})

export const emptyNote = style({
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})
