import { globalStyle, style } from '@vanilla-extract/css'

export const floatingWindow = style({
  position: 'absolute',
  display: 'flex',
  flexDirection: 'column',
  border: '1px solid var(--border)',
  borderRadius: '8px',
  backgroundColor: 'var(--background)',
  boxShadow: '0 8px 32px rgba(0, 0, 0, 0.3), 0 2px 8px rgba(0, 0, 0, 0.2)',
  overflow: 'hidden',
  pointerEvents: 'auto',
})

export const titleBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  height: '32px',
  minHeight: '32px',
  padding: '0 8px',
  backgroundColor: 'var(--muted)',
  borderBottom: '1px solid var(--border)',
  cursor: 'grab',
  userSelect: 'none',
})

export const titleBarDragging = style({
  cursor: 'grabbing',
})

export const titleText = style({
  fontSize: '12px',
  fontWeight: 500,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  flex: 1,
  color: 'var(--foreground)',
})

export const titleCloseButton = style({
  flexShrink: 0,
  marginLeft: '4px',
})

export const windowContent = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

// Resize handles
const resizeHandleBase = style({
  position: 'absolute',
  zIndex: 1,
})

export const resizeN = style([resizeHandleBase, {
  top: '-3px',
  left: '8px',
  right: '8px',
  height: '6px',
  cursor: 'n-resize',
}])

export const resizeS = style([resizeHandleBase, {
  bottom: '-3px',
  left: '8px',
  right: '8px',
  height: '6px',
  cursor: 's-resize',
}])

export const resizeE = style([resizeHandleBase, {
  top: '8px',
  right: '-3px',
  bottom: '8px',
  width: '6px',
  cursor: 'e-resize',
}])

export const resizeW = style([resizeHandleBase, {
  top: '8px',
  left: '-3px',
  bottom: '8px',
  width: '6px',
  cursor: 'w-resize',
}])

export const resizeNE = style([resizeHandleBase, {
  top: '-3px',
  right: '-3px',
  width: '12px',
  height: '12px',
  cursor: 'ne-resize',
}])

export const resizeNW = style([resizeHandleBase, {
  top: '-3px',
  left: '-3px',
  width: '12px',
  height: '12px',
  cursor: 'nw-resize',
}])

export const resizeSE = style([resizeHandleBase, {
  bottom: '-3px',
  right: '-3px',
  width: '12px',
  height: '12px',
  cursor: 'se-resize',
}])

export const resizeSW = style([resizeHandleBase, {
  bottom: '-3px',
  left: '-3px',
  width: '12px',
  height: '12px',
  cursor: 'sw-resize',
}])

// Layer
export const floatingLayer = style({
  position: 'absolute',
  inset: 0,
  pointerEvents: 'none',
  zIndex: 50,
})

// Ensure inner tiles fill their space
globalStyle(`${windowContent} > *`, {
  flex: 1,
  minHeight: 0,
})
