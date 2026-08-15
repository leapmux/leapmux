import { style } from '@vanilla-extract/css'

const dragHandleBase = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'flexShrink': 0,
  'width': '24px',
  'height': '24px',
  // The whole point of the handle: the browser may never turn a press here
  // into a pan, so the pointer stream reaches the drag sensor intact and a
  // touch press-and-move starts the drag immediately.
  'touchAction': 'none',
  'color': 'var(--faint-foreground)',
  'cursor': 'grab',
  ':active': {
    cursor: 'grabbing',
  },
})

/** Always-visible grip, for touch-first surfaces such as the mobile tab sheet. */
export const dragHandle = style([dragHandleBase, {}])

/**
 * Grip that hides wherever the system has a fine pointer. On a mouse-only
 * desktop it never appears (mouse drags rows anywhere); on a hybrid
 * mouse+touchscreen machine the mouse covers reordering, so it stays hidden
 * there too. Only touch-only devices — phones, iPad without a trackpad —
 * see it.
 */
export const dragHandleAuto = style([dragHandleBase, {
  '@media': {
    '(any-pointer: fine)': {
      display: 'none',
    },
  },
}])
