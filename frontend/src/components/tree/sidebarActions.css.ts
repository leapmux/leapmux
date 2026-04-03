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
  transition: 'opacity 0.15s',
  selectors: {
    '&[aria-expanded="true"]': {
      opacity: 1,
    },
  },
})

/** Give sidebarActions a background on hover so it covers scrolled text underneath. */
globalStyle(`:hover > ${sidebarActions}`, {
  backgroundColor: 'inherit',
})

globalStyle(`:hover > ${sidebarActions} ${menuTrigger}`, {
  opacity: 1,
})
