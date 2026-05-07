import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'

export const tilingRoot = style({
  flex: 1,
  overflow: 'hidden',
  height: '100%',
  display: 'flex',
  flexDirection: 'column',
})

// Container + cell styles are shared between SplitRenderer (1D CSS Grid)
// and GridRenderer (2D CSS Grid) — they apply identical box semantics.
// Separators differ per renderer because their attribute selectors do.

export const tilingContainer = style({
  position: 'relative',
  display: 'grid',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
  overflow: 'hidden',
})

export const tilingCell = style({
  position: 'relative',
  minWidth: 0,
  minHeight: 0,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

// Shared separator style for both SplitRenderer and GridRenderer. Both
// emit `data-axis="col"|"row"` on their handles, so one rule covers both.
export const tilingSeparator = style({
  position: 'absolute',
  background: 'transparent',
  // touchAction:'none' so mobile/touch browsers don't scroll or pinch-zoom
  // mid-drag despite our preventDefault() on pointerdown.
  touchAction: 'none',
  zIndex: 6,
  selectors: {
    '&[data-axis="col"]': {
      top: 0,
      bottom: 0,
      width: '4px',
      transform: 'translateX(-2px)',
      cursor: 'col-resize',
    },
    '&[data-axis="row"]': {
      left: 0,
      right: 0,
      height: '4px',
      transform: 'translateY(-2px)',
      cursor: 'row-resize',
    },
    ...resizeHandleSelectors('horizontal', 'var(--border)', '&[data-axis="col"]'),
    ...resizeHandleSelectors('vertical', 'var(--border)', '&[data-axis="row"]'),
  },
})
