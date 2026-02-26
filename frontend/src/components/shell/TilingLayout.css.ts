import { globalStyle, style } from '@vanilla-extract/css'

export const tilingRoot = style({
  flex: 1,
  overflow: 'hidden',
  height: '100%',
  display: 'flex',
  flexDirection: 'column',
})

// Resizable root doesn't set its own height/flex — ensure it fills the tiling root
// and also fills any parent Panel when nested.
globalStyle(`${tilingRoot} [data-corvu-resizable-root]`, {
  flex: 1,
  minHeight: 0,
  minWidth: 0,
})

// Panel elements need to be flex containers so children with height:100% work.
globalStyle(`${tilingRoot} [data-corvu-resizable-panel]`, {
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
})

export const tileResizeHandle = style({
  background: 'transparent',
  borderRadius: 0,
  position: 'relative',
  flexShrink: 0,
  zIndex: 5,
  selectors: {
    '&[data-direction="horizontal"]': {
      width: '4px',
      cursor: 'col-resize',
      margin: '0 -2px',
    },
    '&[data-direction="vertical"]': {
      height: '4px',
      cursor: 'row-resize',
      margin: '-2px 0',
    },
    '&::before': {
      content: '""',
      position: 'absolute',
      background: 'var(--border)',
      transition: 'background 0.15s',
    },
    '&[data-direction="horizontal"]::before': {
      top: 0,
      bottom: 0,
      left: '50%',
      width: '1px',
      transform: 'translateX(-50%)',
    },
    '&[data-direction="vertical"]::before': {
      left: 0,
      right: 0,
      top: '50%',
      height: '1px',
      transform: 'translateY(-50%)',
    },
    '&[data-direction="horizontal"]:hover::before': {
      background: 'var(--border)',
      width: '4px',
    },
    '&[data-direction="vertical"]:hover::before': {
      background: 'var(--border)',
      height: '4px',
    },
    '&[data-direction="horizontal"]:active::before': {
      background: 'var(--primary)',
      width: '1px',
    },
    '&[data-direction="vertical"]:active::before': {
      background: 'var(--primary)',
      height: '1px',
    },
  },
})
