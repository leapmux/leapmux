import { style } from '@vanilla-extract/css'
import { node } from '../tree/sharedTree.css'

export const treeWrapper = style({
  paddingBottom: 'var(--space-1)',
})

export const leafNode = style({
  paddingRight: 'var(--space-1)',
  selectors: {
    [`${node}&:hover`]: {
      backgroundColor: 'var(--card)',
    },
  },
})

export const leafActive = style({
  backgroundColor: 'var(--secondary)',
  selectors: {
    [`${node}&:hover`]: {
      backgroundColor: 'var(--muted)',
    },
  },
})

export const leafDragging = style({
  opacity: 0.4,
})

export const groupIcon = style({
  flexShrink: 0,
  color: 'var(--primary)',
})

export const tabIcon = style({
  flexShrink: 0,
})

export const tabLabel = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  // Natural content width (capped by row via flex-shrink) — not flex: 1 —
  // so getBoundingClientRect on this span reflects where the text actually
  // is. The tooltip centers over the rect; flex: 1 would stretch this into
  // the trailing empty space and place the tooltip far to the right. The
  // close button in sidebarActions still sits flush-right via margin-left:
  // auto, so no layout shift.
  flex: '0 1 auto',
  minWidth: 0,
})

export const leafActions = style({
  minWidth: 0,
})

export const tabRenameInput = style({
  'flex': 1,
  'fontSize': 'var(--text-7)',
  'fontWeight': 400,
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'padding': '0 var(--space-1)',
  'outline': 'none',
  'minWidth': 0,
  ':focus': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})
