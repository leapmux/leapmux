import { globalStyle, style } from '@vanilla-extract/css'

/** Container for action buttons (e.g., context menu trigger) that sticks to the right edge. */
export const sidebarActions = style({
  display: 'flex',
  alignItems: 'center',
  flexShrink: 0,
  marginLeft: 'auto',
  position: 'sticky',
  right: 'var(--space-2)',
  backgroundColor: 'transparent',
})

/** Menu trigger button — hidden until parent hover or menu open. */
export const menuTrigger = style({
  opacity: 0,
  transition: 'opacity var(--transition)',
  selectors: {
    '&[aria-expanded="true"]': {
      opacity: 1,
    },
  },
})

/**
 * A slot where a resting indicator and the row's `menuTrigger` share ONE cell.
 *
 * Both children sit in the same grid area, so swapping them cannot change the
 * row's width -- side-by-side would make every row jump sideways on hover. The
 * indicator rests where the trigger will appear, which is also why it is the
 * right edge that stays visually stable.
 */
export const actionSlot = style({
  display: 'grid',
  gridTemplateAreas: '"slot"',
  alignItems: 'center',
  justifyItems: 'center',
})

globalStyle(`${actionSlot} > *`, {
  gridArea: 'slot',
})

// The trigger paints above the resting indicator whatever the source order, so
// its whole hit area stays live while the indicator fades out over it.
globalStyle(`${actionSlot} ${menuTrigger}`, {
  position: 'relative',
  zIndex: 1,
})

/**
 * The resting indicator inside an `actionSlot`: visible until the row is
 * hovered or its menu is open, at which point the trigger takes the cell.
 * Exactly the inverse of `menuTrigger`, so the two are never both visible and
 * never both absent.
 */
export const actionSlotResting = style({
  transition: 'opacity var(--transition)',
  // NEVER a click target. It shares its cell with the trigger, and an element
  // at `opacity: 0` still receives pointer events -- so without this the faded
  // indicator sits in front of the trigger and swallows the click aimed at it,
  // which reads as a three-dot menu that is visible but does not open. The
  // indicator is decorative (the row also carries its state as data-status), so
  // it gives up hit-testing at no cost.
  pointerEvents: 'none',
})

globalStyle(`:hover > ${sidebarActions} ${actionSlotResting}`, {
  opacity: 0,
})

// The menu can be open without a hover (opened by keyboard, or the pointer
// moved away while it is up). `:has` keeps the pair in step for that case too.
globalStyle(`${actionSlot}:has([aria-expanded="true"]) ${actionSlotResting}`, {
  opacity: 0,
})

/** Give sidebarActions a background on hover so it covers scrolled text underneath. */
globalStyle(`:hover > ${sidebarActions}`, {
  backgroundColor: 'inherit',
})

globalStyle(`:hover > ${sidebarActions} ${menuTrigger}`, {
  opacity: 1,
})
