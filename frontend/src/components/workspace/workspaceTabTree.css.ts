import { style } from '@vanilla-extract/css'
import { node } from '../tree/sharedTree.css'

export const treeWrapper = style({
  paddingBottom: 'var(--space-1)',
})

export const leafNode = style({
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
  flex: 1,
  minWidth: 0,
})

export const groupLabel = style({
  whiteSpace: 'nowrap',
})
