import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const pathBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-2) var(--space-3)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  minHeight: '32px',
})

export const breadcrumbList = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  listStyle: 'none',
  margin: 0,
  padding: 0,
})

export const pathSegment = style({
  'all': 'unset',
  'fontSize': 'var(--text-7)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  ':hover': {
    color: 'var(--primary)',
  },
})

export const pathSeparator = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
})

export const fileList = style({
  flex: 1,
  overflow: 'auto',
  padding: 'var(--space-1) 0',
})

export const fileItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'padding': 'var(--space-1) var(--space-3)',
  'cursor': 'pointer',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const fileIcon = style({
  flexShrink: 0,
  width: '16px',
  textAlign: 'center',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})

export const dirIcon = style([fileIcon, {
  color: 'var(--primary)',
}])

export const fileName = style({
  flex: 1,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const fileSize = style({
  flexShrink: 0,
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  minWidth: '50px',
  textAlign: 'right',
})

export const loadingState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
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
