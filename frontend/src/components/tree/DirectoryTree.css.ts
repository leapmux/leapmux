import { globalStyle, style } from '@vanilla-extract/css'

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

export const pathInput = style({
  display: 'flex',
  alignItems: 'center',
  padding: 'var(--space-1)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
})

// Oat's default `input` style sets `margin-block-start: var(--space-1)` for
// form-field spacing under a label. That's not wanted here — the input sits
// alone in a flex row and we want a uniform --space-1 gap on all four sides
// of the input (provided by pathInput's padding).
globalStyle(`${pathInput} input`, {
  marginBlockStart: 0,
})

export const pathHint = style({
  fontSize: 'var(--text-8)',
  color: 'var(--warning-foreground, var(--faint-foreground))',
  padding: '2px var(--space-2) 0',
  lineHeight: 1.2,
})
