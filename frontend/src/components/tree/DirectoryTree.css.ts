import { style } from '@vanilla-extract/css'

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

// One sizing rule, because both parents are flex columns: the sidebar's
// `treeContent` and the dialog's `treeContainer`. `flex: 1` claims whatever the
// parent has left -- the whole box in the sidebar, and what the path input
// leaves in the dialog.
export const container = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
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

export const nodeNameMuted = style([nodeName, {
  color: 'var(--muted-foreground)',
}])

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

export const emptyInline = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: '2px var(--space-2)',
})
