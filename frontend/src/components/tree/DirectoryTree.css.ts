import { style } from '@vanilla-extract/css'
import { iconSize, spacing } from '~/styles/tokens'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const tree = style({
  flex: 1,
  overflow: 'auto',
  padding: `${spacing.xs} 0`,
})

export const node = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': '4px',
  'padding': `2px ${spacing.sm}`,
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
  flexShrink: 0,
  color: 'var(--muted-foreground)',
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
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  flex: 1,
})

export const loadingState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: spacing.xl,
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})

export const loadingInline = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: `2px ${spacing.sm}`,
})

export const errorState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: spacing.xl,
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})

export const emptyState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: spacing.xl,
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
})

export const emptyInline = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: `2px ${spacing.sm}`,
})

export const nodeActions = style({
  display: 'flex',
  alignItems: 'center',
  marginLeft: 'auto',
  flexShrink: 0,
  opacity: 0,
  transition: 'opacity 0.15s',
  selectors: {
    [`${node}:hover &`]: { opacity: 1 },
  },
})

export const nodeActionButton = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': iconSize.container.sm,
  'height': iconSize.container.sm,
  'borderRadius': '3px',
  'flexShrink': 0,
  'color': 'var(--faint-foreground)',
  'cursor': 'pointer',
  ':hover': { color: 'var(--foreground)' },
})

export const pathInput = style({
  display: 'flex',
  alignItems: 'center',
  padding: `${spacing.xs} ${spacing.sm}`,
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
})

export const pathInputField = style({
  'all': 'unset',
  'flex': 1,
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'fontFamily': 'var(--font-mono)',
  'padding': `${spacing.xs} ${spacing.sm}`,
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
