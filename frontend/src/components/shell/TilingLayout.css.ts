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
  selectors: {
    '&[data-direction="horizontal"]': {
      width: '6px',
      cursor: 'col-resize',
    },
    '&[data-direction="vertical"]': {
      height: '6px',
      cursor: 'row-resize',
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
    '&:hover::before': {
      background: 'var(--primary)',
    },
    '&:active::before': {
      background: 'var(--primary)',
    },
  },
})
