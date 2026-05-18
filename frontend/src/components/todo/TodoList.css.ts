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

export const todoStruck = style({
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
  // Nudges the 1rem checkbox down a sub-pixel so it visually sits on
  // the text baseline of the adjacent label instead of floating above it.
  marginTop: '0.25px',
})

export const todoText = style({
  flex: 1,
  wordBreak: 'break-word',
})
