import { globalStyle, style } from '@vanilla-extract/css'
import { node, nodeSelected } from './sharedTree.css'

export {
  chevron,
  chevronExpanded,
  chevronPlaceholder,
  childrenInner,
  childrenWrapper,
  childrenWrapperExpanded,
  node,
  nodeSelected,
} from './sharedTree.css'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const tree = style({
  flex: 1,
  overflow: 'auto',
  padding: 'var(--space-1) 0',
})

/** Inner wrapper that sizes to the widest node, enabling horizontal scroll. */
export const treeInner = style({
  minWidth: '100%',
  width: 'max-content',
})

export const folderIcon = style({
  flexShrink: 0,
  color: 'var(--primary)',
})

export const fileIcon = style({
  flexShrink: 0,
  color: 'var(--muted-foreground)',
})

// Git-status icon color overrides (applied to folder/file icons).
export const iconStaged = style({ color: 'var(--success)' })
export const iconUnstaged = style({ color: 'var(--warning)' })
export const iconUntracked = style({ color: 'var(--success)' })
export const iconConflict = style({ color: 'var(--danger)' })
export const iconDirChanged = style({ color: 'var(--warning)', opacity: 0.85 })

export const nodeName = style({
  whiteSpace: 'nowrap',
})

export const loadingState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})

export const loadingInline = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: '2px var(--space-2)',
})

export const errorState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})

export const emptyState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})

export const emptyInline = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: '2px var(--space-2)',
})

export const nodeActions = style({
  display: 'flex',
  alignItems: 'center',
  marginLeft: 'auto',
  flexShrink: 0,
  position: 'sticky',
  right: 'var(--space-2)',
})

export const nodeMenuTrigger = style({
  opacity: 0,
  transition: 'opacity 0.15s',
  selectors: {
    [`${node}:hover &`]: { opacity: 1 },
    '&[aria-expanded="true"]': { opacity: 1 },
  },
})

/** Give nodeActions a background on hover so it covers scrolled text underneath. */
globalStyle(`${node}:hover ${nodeActions}`, {
  backgroundColor: 'var(--card)',
})

/** Match selected node background. */
globalStyle(`${node}${nodeSelected} ${nodeActions}`, {
  backgroundColor: 'var(--secondary)',
})

/** Match selected + hover node background. */
globalStyle(`${node}${nodeSelected}:hover ${nodeActions}`, {
  backgroundColor: 'var(--muted)',
})

export const pathInput = style({
  display: 'flex',
  alignItems: 'center',
  padding: 'var(--space-1) var(--space-2)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
})

export const pathInputField = style({
  'all': 'unset',
  'flex': 1,
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'fontFamily': 'var(--font-mono)',
  'padding': 'var(--space-1) var(--space-2)',
  'borderRadius': 'var(--radius-small)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--border)',
  'boxSizing': 'border-box',
  ':focus': {
    borderColor: 'var(--ring)',
    boxShadow: '0 0 0 2px var(--ring)',
  },
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})
