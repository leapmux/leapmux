import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
  minWidth: 0,
})

export const toolbar = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-2)',
})

export const workerSelect = style({
  minWidth: '180px',
})

export const toggleRow = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  padding: 'var(--space-2) var(--space-3)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  fontSize: 'var(--text-7)',
})

export const targetsRow = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})

export const targetCheck = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  color: 'var(--foreground)',
  cursor: 'pointer',
})

export const skillList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const skillRow = style({
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  padding: 'var(--space-2) var(--space-3)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const skillBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  minWidth: 0,
  flex: 1,
})

export const skillHeader = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const skillName = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  color: 'var(--foreground)',
})

export const skillState = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const skillDesc = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  lineHeight: 1.4,
})

export const skillTargetDetails = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  lineHeight: 1.4,
})

export const hint = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const empty = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  fontStyle: 'italic',
})
