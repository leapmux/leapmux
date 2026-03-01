import { globalStyle, style } from '@vanilla-extract/css'

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

export const node = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': '4px',
  'padding': '2px var(--space-2)',
  'cursor': 'pointer',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'userSelect': 'none',
  'whiteSpace': 'nowrap',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const nodeSelected = style({
  backgroundColor: 'var(--secondary)',
  selectors: {
    '&:hover': {
      backgroundColor: 'var(--muted)',
    },
  },
})

export const chevron = style({
  'flexShrink': 0,
  'color': 'var(--muted-foreground)',
  'transition': 'transform 150ms cubic-bezier(0.4, 0, 0.2, 1)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const chevronExpanded = style({
  transform: 'rotate(90deg)',
})

export const chevronPlaceholder = style({
  flexShrink: 0,
  width: '16px',
})

export const folderIcon = style({
  flexShrink: 0,
  color: 'var(--primary)',
})

export const fileIcon = style({
  flexShrink: 0,
  color: 'var(--muted-foreground)',
})

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

export const childrenWrapper = style({
  'display': 'grid',
  'gridTemplateRows': '0fr',
  'visibility': 'hidden',
  'transition': 'grid-template-rows 150ms cubic-bezier(0.4, 0, 0.2, 1), visibility 150ms',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const childrenWrapperExpanded = style({
  gridTemplateRows: '1fr',
  visibility: 'visible',
})

export const childrenInner = style({
  overflow: 'clip',
  minHeight: 0,
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
