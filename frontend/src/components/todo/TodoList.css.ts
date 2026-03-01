import { style } from '@vanilla-extract/css'

export const todoList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: 'var(--space-1) var(--space-2)',
})

export const todoItem = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: 'var(--space-2)',
  padding: '3px 0',
  fontSize: 'var(--text-7)',
  lineHeight: 1.4,
  color: 'var(--foreground)',
})

export const todoCompleted = style({
  color: 'var(--muted-foreground)',
  textDecoration: 'line-through',
})

export const todoInProgress = style({
  color: 'var(--primary)',
})

export const todoIcon = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flexShrink: 0,
  width: '18px',
  height: '20px',
})

export const checkIcon = style({
  color: 'var(--success)',
})

export const spinnerIcon = style({
  color: 'var(--primary)',
})

export const pendingIcon = style({
  color: 'var(--faint-foreground)',
})

export const todoText = style({
  flex: 1,
  wordBreak: 'break-word',
})
